// Package archive encodes and decodes the fontvault repository's family
// archives: a tar stream compressed with Brotli, written entirely in Go.
// Fonts are never held in the repository as loose files: one archive per
// font family, even when the family has a single font.
//
// Brotli at quality 11 matches 7-Zip's LZMA2 on TrueType data to within one
// or two percent (Brotli ships a dictionary built for fonts; WOFF2 uses it),
// which is why it replaced the external 7z tool without giving up space. The
// archive is solid (one stream over the whole tar), so members compress
// against each other, and the encoder is deterministic: the same members
// always produce the same bytes on every machine.
package archive

import (
	"archive/tar"
	"bytes"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/andybalholm/brotli"
)

// Ext is the family archive file extension.
const Ext = ".tar.br"

const (
	// Quality is the Brotli level. 11 is the maximum; 10 is about twice as
	// fast for one to two percent more space, 9 is an order of magnitude
	// faster for five percent more.
	Quality = 11
	// windowBits is the Brotli sliding window (2^24 = 16 MiB), the largest
	// the format allows without the large-window extension, so a whole
	// family stays inside one window.
	windowBits = 24
)

// epoch is the modification time every tar entry carries, so the archive
// bytes never depend on when they were written.
var epoch = time.Unix(0, 0).UTC()

// Encode returns the archive holding members, keyed by member name. Names
// are written in sorted order with fixed metadata, so equal input gives
// equal output. An empty set or a name containing a path separator is an
// error: members are flat names, never paths.
func Encode(members map[string][]byte) ([]byte, error) {
	if len(members) == 0 {
		return nil, errors.New("encode archive: no members")
	}
	names := make([]string, 0, len(members))
	for name := range members {
		if name == "" || strings.ContainsAny(name, `/\`) {
			return nil, fmt.Errorf("encode archive: invalid member name %q", name)
		}
		names = append(names, name)
	}
	sort.Strings(names)

	var out bytes.Buffer
	bw := brotli.NewWriterOptions(&out, brotli.WriterOptions{Quality: Quality, LGWin: windowBits})
	if err := writeTar(bw, names, members); err != nil {
		_ = bw.Close() // best effort: the partial output is discarded with the error
		return nil, err
	}
	if err := bw.Close(); err != nil {
		return nil, fmt.Errorf("encode archive: close Brotli: %w", err)
	}
	return out.Bytes(), nil
}

// writeTar writes members to w as a tar stream in the given name order. The
// tar writer is closed on every path; a close failure is reported only when
// nothing failed before it.
func writeTar(w io.Writer, names []string, members map[string][]byte) (err error) {
	tw := tar.NewWriter(w)
	defer func() {
		if curErr := tw.Close(); curErr != nil && err == nil {
			err = fmt.Errorf("encode archive: close tar: %w", curErr)
		}
	}()
	for _, name := range names {
		hdr := &tar.Header{
			Typeflag: tar.TypeReg,
			Name:     name,
			Mode:     0o644,
			Size:     int64(len(members[name])),
			ModTime:  epoch,
			Format:   tar.FormatUSTAR,
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return fmt.Errorf("encode archive: header for %s: %w", name, err)
		}
		if _, err := tw.Write(members[name]); err != nil {
			return fmt.Errorf("encode archive: write %s: %w", name, err)
		}
	}
	return nil
}

// Decode returns every member of the archive, keyed by name.
func Decode(data []byte) (map[string][]byte, error) {
	members := map[string][]byte{}
	tr := tar.NewReader(brotli.NewReader(bytes.NewReader(data)))
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return members, nil
		}
		if err != nil {
			return nil, fmt.Errorf("decode archive: %w", err)
		}
		if hdr.Typeflag != tar.TypeReg {
			return nil, fmt.Errorf("decode archive: %s is not a regular file", hdr.Name)
		}
		content, err := io.ReadAll(tr)
		if err != nil {
			return nil, fmt.Errorf("decode archive: read %s: %w", hdr.Name, err)
		}
		members[hdr.Name] = content
	}
}

// Members returns the member names stored in the archive, sorted.
func Members(data []byte) ([]string, error) {
	members, err := Decode(data)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(members))
	for name := range members {
		names = append(names, name)
	}
	sort.Strings(names)
	return names, nil
}

// Extract returns the bytes stored under member, or an error when the
// archive has no such member.
func Extract(data []byte, member string) ([]byte, error) {
	members, err := Decode(data)
	if err != nil {
		return nil, err
	}
	content, ok := members[member]
	if !ok {
		return nil, fmt.Errorf("extract %s: no such member in archive", member)
	}
	return content, nil
}
