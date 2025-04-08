package images

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"

	"github.com/containerd/containerd/cmd/ctr/commands"
	"github.com/containerd/containerd/content"
	"github.com/containerd/containerd/images"
	"github.com/containerd/containerd/labels"
	"github.com/containerd/containerd/snapshots"
	"github.com/containerd/log"
	"github.com/containerd/platforms"
	"github.com/opencontainers/go-digest"
	"github.com/opencontainers/image-spec/identity"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/urfave/cli"
	"golang.org/x/sync/errgroup"
)

var verifyCommand = cli.Command{
	Name:      "verify",
	Usage:     "Verify the image files is correct",
	ArgsUsage: "[flags] <name>",
	Action: func(clicontext *cli.Context) error {
		if clicontext.NArg() != 1 {
			return cli.ShowCommandHelp(clicontext, "verify")
		}
		name := clicontext.Args().Get(0)

		client, ctx, cancel, err := commands.NewClient(clicontext)
		if err != nil {
			return err
		}
		defer cancel()

		imageStore := client.ImageService()
		image, err := imageStore.Get(ctx, name)
		if err != nil {
			return fmt.Errorf("failed to get image %s: %w", name, err)
		}
		provider := client.ContentStore()
		// equivalent to `nerdctl inspect docker.io/crazytaxii/network-tester:latest --format '{{json .}}' | jq -r '.RootFS.Layers'`
		diffIDs, err := image.RootFS(ctx, provider, platforms.Default())
		if err != nil {
			return err
		}

		m := make(map[string]int)                  // index of mappings slice
		mappings := make([]*Mapping, len(diffIDs)) // image layers info slice
		for i, d := range diffIDs {
			m[d.String()] = i // diffID as index
			mappings[i] = &Mapping{
				DiffID: d.String(),
			}
		}

		// equivalent to `ctr content ls`
		if err := provider.Walk(ctx, func(info content.Info) (_ error) {
			uncompressed, ok := info.Labels[labels.LabelUncompressed]
			if !ok {
				return
			}

			// get index
			i, ok := m[uncompressed]
			if !ok {
				return
			}
			mappings[i].BlobDigest = info.Digest
			return
		}); err != nil {
			return err
		}

		if err := fillDescriptorAndVerifyBlob(ctx, provider, image, mappings); err != nil {
			return err
		}

		snapshotter := client.SnapshotService(clicontext.GlobalString("snapshotter"))
		toParents := make(map[string]snapshots.Info)
		// equivalent to `ctr snapshots ls`
		if err := snapshotter.Walk(ctx, func(ctx context.Context, info snapshots.Info) (_ error) {
			_, ok := toParents[info.Name]
			if ok {
				log.G(ctx).Warnf("duplicate snapshot key %s", info.Name)
			}
			toParents[info.Name] = info
			return
		}); err != nil {
			return err
		}

		if err := getUpperDirs(toParents, mappings, diffIDs); err != nil {
			return err
		}

		eg, ctx2 := errgroup.WithContext(ctx)
		for _, mapping := range mappings {
			mapping := mapping
			eg.Go(func() error {
				return verifyImageLayer(ctx2, provider, mapping.Descriptor, mapping.UpperDir, mapping.DiffID)
			})
		}
		return eg.Wait()
	},
}

type Mapping struct {
	DiffID      string
	BlobDigest  digest.Digest
	SnapshotKey string
	UpperDir    string // overlayfs upperdir
	// It is used to read blob from content store
	ocispec.Descriptor // oci descriptor
}

func getUpperDirs(toParents map[string]snapshots.Info, mappings []*Mapping, diffIDs []digest.Digest) error {
	index := len(mappings) - 1
	current := identity.ChainID(diffIDs).String()
	for {
		info := toParents[current]
		if mappings[index].UpperDir = info.Labels["containerd.io/snapshot/overlay.upperdir"]; mappings[index].UpperDir == "" {
			// `upperdir_label` in overlay plugin config must be set with true
			return fmt.Errorf("upperdir_label is disabled in overlay plugin config")
		}
		mappings[index].SnapshotKey = info.Name

		if index == 0 {
			break
		}
		current = info.Parent
		index--
	}
	return nil
}

func verifyImageLayer(ctx context.Context, cs content.Store, desc ocispec.Descriptor, upperDir, diffID string) (err error) {
	// create a temporary directory for unarchiving
	tmp, err := os.MkdirTemp("/tmp", "ctr-image-verify-")
	if err != nil {
		return err
	}
	defer func() {
		if err == nil {
			_ = os.RemoveAll(tmp)
		}
	}()

	// e.g. /var/lib/containerd/io.containerd.content.v1.content/blobs/sha256/0d730e148c105da59c5f207752779797679cb3b7cc473b9123d7faff3ae7fce2
	ra, err := cs.ReaderAt(ctx, desc)
	if err != nil {
		return err
	}
	defer ra.Close()
	cr := content.NewReader(ra)
	// unarchive the raw blob file to temp directory
	if err = extract(ctx, cr, tmp); err != nil {
		return
	}

	// diff
	log.G(ctx).Infof("diff image layer %s and blob %s (diffID: %s)", upperDir, desc.Digest.String(), diffID)
	return diff(tmp, upperDir)
}

func fillDescriptorAndVerifyBlob(ctx context.Context, cs content.Store, image images.Image, mappings []*Mapping) error {
	m := make(map[string]int) // index of mappings slice
	for i, mapping := range mappings {
		m[mapping.BlobDigest.String()] = i // blob digest as index
	}
	manifest, err := images.Manifest(ctx, cs, image.Target, platforms.Default())
	if err != nil {
		return err
	}
	// layers in image manifest
	for _, layer := range manifest.Layers {
		i, ok := m[layer.Digest.String()]
		if !ok {
			continue
		}
		// fill the oci descriptor
		mappings[i].Descriptor = layer

		// verify the blob file itself
		ra, err := cs.ReaderAt(ctx, layer)
		if err != nil {
			return err
		}
		defer ra.Close()
		cr := content.NewReader(ra)

		digest, err := computeDigest(cr)
		if err != nil {
			return err
		}
		log.G(ctx).Infof("blob %s digest %s", layer.Digest.String(), digest)
		if digest != layer.Digest.Encoded() {
			// the blob file is damaged, it's unnecessary to verify the image layer
			return fmt.Errorf("blob %s is damaged", layer.Digest.String())
		}
	}
	return nil
}

// computeDigest returns the sha256 hash of the file
func computeDigest(r io.Reader) (digest string, err error) {
	hasher := sha256.New()
	if _, err = io.Copy(hasher, r); err != nil {
		return
	}
	return hex.EncodeToString(hasher.Sum(nil)), nil
}
