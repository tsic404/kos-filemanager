// Command kos-filemanager-pack builds the installable extension package of the
// file manager through the SDK's one packing path (KOSD-C-17, SDK-C-4).
//
// The artifact is what an install request names: the verified package bytes and
// the digest that covers its manifest and every asset.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/tsic404/kos-sdk/packaging"

	"github.com/tsic404/kos-filemanager/pkg/product"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "kos-filemanager-pack:", err)
		os.Exit(1)
	}
}

func run() error {
	origin := flag.String("origin", product.DefaultEntryOrigin, "application entry origin (absolute https)")
	out := flag.String("out", "dist", "directory the artifact and its digest are written to")
	flag.Parse()

	pkg, archive, err := product.Build(*origin)
	if err != nil {
		return err
	}
	// Verify what is about to leave the process with the same check an install
	// path runs, so a package that cannot be installed is never written.
	if _, err := packaging.Verify(archive, pkg.Digest); err != nil {
		return fmt.Errorf("verify the packed artifact: %w", err)
	}
	if err := os.MkdirAll(*out, 0o755); err != nil {
		return err
	}
	base := fmt.Sprintf("%s-%s", product.Identifier, product.Version)
	archivePath := filepath.Join(*out, base+".tar.gz")
	if err := os.WriteFile(archivePath, archive, 0o644); err != nil {
		return err
	}
	digestPath := filepath.Join(*out, base+".digest")
	if err := os.WriteFile(digestPath, []byte(pkg.Digest.String()+"\n"), 0o644); err != nil {
		return err
	}
	fmt.Println(pkg.Digest)
	return nil
}
