// Copyright 2013 Richard Lehane. All rights reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Package mscfb implements a reader for Microsoft's Compound File Binary File Format (http://msdn.microsoft.com/en-us/library/dd942138.aspx).
//
// The Compound File Binary File Format is also known as the Object Linking and Embedding (OLE) or Component Object Model (COM) format and was used by many
// early MS software such as MS Office.
//
// Example:
//   file, _ := os.Open("test/test.doc")
//   defer file.Close()
//   doc, err := mscfb.NewReader(file)
//   if err != nil {
//     log.Fatal(err)
//	 }
//	 for entry, err := doc.Next(); err == nil; entry, err = doc.Next() {
//     buf := make([]byte, 512)
//     i, _ := doc.Read(buf)
//     if i > 0 {
//       fmt.Println(buf[:i])
//     }
//     fmt.Println(entry.Name)
//   }
package mscfb

import (
	"encoding/binary"
	"errors"
	"io"
)

var (
	ErrFormat   = errors.New("mscfb: not a valid compound file")
	ErrRead     = errors.New("mscfb: error reading compound file")
	ErrNoStream = errors.New("mscfb: storage object does not have a child stream")
)

var sectorSize uint32

func setSectorSize(ss uint16) {
	sectorSize = uint32(1 << ss)
}

const (
	signature            uint64 = 0xE11AB1A1E011CFD0
	miniStreamSectorSize uint32 = 64
	miniStreamCutoffSize uint64 = 4096
	dirEntrySize         uint32 = 128 //128 bytes
)

const (
	maxRegSect     uint32 = 0xFFFFFFFA // Maximum regular sector number
	difatSect      uint32 = 0xFFFFFFFC //Specifies a DIFAT sector in the FAT
	fatSect        uint32 = 0xFFFFFFFD // Specifies a FAT sector in the FAT
	endOfChain     uint32 = 0xFFFFFFFE // End of linked chain of sectors
	freeSect       uint32 = 0xFFFFFFFF // Speficies unallocated sector in the FAT, Mini FAT or DIFAT
	maxRegStreamID uint32 = 0xFFFFFFFA // maximum regular stream ID
	noStream       uint32 = 0xFFFFFFFF // empty pointer
)

func (r *Reader) binaryReadAt(offset int64, data interface{}) error {
	if _, err := r.rs.Seek(offset, 0); err != nil {
		return ErrRead
	}
	if err := binary.Read(r.rs, binary.LittleEndian, data); err != nil {
		return ErrRead
	}
	return nil
}

func (r *Reader) rawReadAt(b []byte, offset int64) error {
	if _, err := r.rs.Seek(offset, 0); err != nil {
		return ErrRead
	}
	if _, err := r.rs.Read(b); err != nil {
		return ErrRead
	}
	return nil
}

func (r *Reader) fileOffset(sn uint32, mini bool) int64 {
	if mini {
		num := sectorSize / 64
		sec := sn / num
		dif := sn % num
		return int64((r.header.miniStreamLocs[sec]+1)*sectorSize + dif*64)
	}
	return int64((sn + 1) * sectorSize)
}

// check the FAT sector for the next sector in a chain
func (r *Reader) findNext(sn uint32, mini bool) (uint32, error) {
	entries := sectorSize / 4
	index := int(sn / entries) // find position in DIFAT or minifat array
	var sect uint32
	if mini {
		sect = r.header.miniFatLocs[index]
	} else {
		sect = r.header.difats[index]
	}
	fatIndex := sn % entries // find position within FAT or MiniFAT sector
	offset := r.fileOffset(sect, false)
	offset += int64(fatIndex * 4)
	var target uint32
	err := r.binaryReadAt(offset, &target)
	return target, err
}

// Reader provides sequential access to the contents of a compound file
type Reader struct {
	header  *header
	entries []*DirectoryEntry
	rs      io.ReadSeeker
	entry   int
	stream  [][2]int64 // contains file offsets for the current stream and lengths
}

func NewReader(rs io.ReadSeeker) (*Reader, error) {
	r := new(Reader)
	r.rs = rs
	if err := r.setHeader(); err != nil {
		return nil, err
	}
	if err := r.setDirEntries(); err != nil {
		return nil, err
	}
	if err := r.setMiniStream(); err != nil {
		return nil, err
	}
	return r, nil
}

func (r *Reader) Next() (*DirectoryEntry, error) {
	if r.entry >= len(r.entries)-1 {
		return nil, io.EOF
	}
	r.entry += 1
	entry := r.entries[r.entry]
	if entry.StartingSectorLoc <= maxRegSect {
		var mini bool
		if entry.StreamSize < miniStreamCutoffSize {
			mini = true
		}
		err := r.setStream(entry.StartingSectorLoc, entry.StreamSize, mini)
		if err != nil {
			return nil, err
		}
	}
	return entry, nil
}

func (r *Reader) Read(b []byte) (n int, err error) {
	if r.entry == 0 || r.entries[r.entry].StartingSectorLoc == noStream {
		return 0, ErrNoStream
	}
	if len(r.stream) == 0 {
		return 0, io.EOF
	}
	stream, sz := r.popStream(cap(b))
	var idx int64
	for _, v := range stream {
		jdx := idx + v[1]
		err := r.rawReadAt(b[idx:jdx], v[0])
		if err != nil {
			return 0, err
		}
		idx += v[1]
	}
	return sz, nil
}
