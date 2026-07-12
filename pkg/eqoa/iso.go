package eqoa

import (
	"encoding/binary"
	"fmt"
	"io"
	"strings"
)

const (
	isoSectorSize = 2048
	isoPVDSector  = 16
)

// ISOFile is a file entry found in an ISO 9660 filesystem.
type ISOFile struct {
	Path   string // full uppercase path, e.g. "/ASSETS/ARENA.CSF"
	Offset int64  // byte offset within the ISO image
	Size   int64  // file size in bytes
}

// ReadISOFiles scans an ISO 9660 image and returns every file for which
// filter(uppercasePath) is true.  r must support random access reads.
func ReadISOFiles(r io.ReaderAt, filter func(path string) bool) ([]ISOFile, error) {
	pvd := make([]byte, isoSectorSize)
	if _, err := r.ReadAt(pvd, int64(isoPVDSector*isoSectorSize)); err != nil {
		return nil, fmt.Errorf("reading PVD: %w", err)
	}
	if string(pvd[1:6]) != "CD001" {
		return nil, fmt.Errorf("not an ISO 9660 image (bad signature)")
	}

	rootExtent := int64(binary.LittleEndian.Uint32(pvd[156+2:]))
	rootSize := int64(binary.LittleEndian.Uint32(pvd[156+10:]))

	var files []ISOFile
	var walkDir func(sector, size int64, prefix string) error
	walkDir = func(sector, size int64, prefix string) error {
		dir := make([]byte, size)
		if _, err := r.ReadAt(dir, sector*isoSectorSize); err != nil {
			return fmt.Errorf("reading directory sector %d: %w", sector, err)
		}

		pos := 0
		for pos < len(dir) {
			recLen := int(dir[pos])
			if recLen == 0 {
				// Zero-padding: advance to the next sector boundary.
				next := ((pos / isoSectorSize) + 1) * isoSectorSize
				if next >= len(dir) {
					break
				}
				pos = next
				continue
			}
			if pos+recLen > len(dir) {
				break
			}

			rec := dir[pos : pos+recLen]
			extSector := int64(binary.LittleEndian.Uint32(rec[2:]))
			extSize := int64(binary.LittleEndian.Uint32(rec[10:]))
			flags := rec[25]
			nameLen := int(rec[32])
			if 33+nameLen > len(rec) {
				pos += recLen
				continue
			}
			name := string(rec[33 : 33+nameLen])

			// Skip the "." and ".." entries.
			if name != "\x00" && name != "\x01" {
				// Strip the ISO 9660 version suffix (;1).
				if i := strings.IndexByte(name, ';'); i >= 0 {
					name = name[:i]
				}
				fullPath := prefix + "/" + strings.ToUpper(name)

				if flags&0x02 != 0 {
					if err := walkDir(extSector, extSize, fullPath); err != nil {
						return err
					}
				} else if filter(fullPath) {
					files = append(files, ISOFile{
						Path:   fullPath,
						Offset: extSector * isoSectorSize,
						Size:   extSize,
					})
				}
			}
			pos += recLen
		}
		return nil
	}

	if err := walkDir(rootExtent, rootSize, ""); err != nil {
		return nil, err
	}
	return files, nil
}

// ReadAll reads the entire file content from the ISO image.
func (f *ISOFile) ReadAll(r io.ReaderAt) ([]byte, error) {
	buf := make([]byte, f.Size)
	_, err := r.ReadAt(buf, f.Offset)
	return buf, err
}
