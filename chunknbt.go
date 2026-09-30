package main

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"

	"github.com/klauspost/compress/gzip"
	"github.com/klauspost/compress/zlib"
)

const (
	tagEnd = iota
	tagByte
	tagShort
	tagInt
	tagLong
	tagFloat
	tagDouble
	tagByteArray
	tagString
	tagList
	tagCompound
	tagIntArray
	tagLongArray
)

// maxNBTDepth matches the nesting limit Minecraft enforces when reading NBT.
const maxNBTDepth = 512

var errNBTTruncated = errors.New("nbt: unexpected end of data")

// longArray is an NBT long array left in its big-endian wire form, so it can be
// read in place instead of being copied into a []uint64.
type longArray []byte

func (a longArray) Len() int { return len(a) / 8 }

func (a longArray) At(i int) uint64 { return binary.BigEndian.Uint64(a[i*8:]) }

type chunkSection struct {
	Y int
	// Palette holds block names only; block state properties are skipped.
	Palette      [][]byte
	Data         longArray
	BiomePalette [][]byte
	BiomeData    longArray
}

// chunkData holds the fields of a chunk that the renderer needs. Every byte
// slice in it aliases the chunkDecoder's buffer and is only valid until the
// next call to decode.
type chunkData struct {
	YPos           int
	MotionBlocking longArray
	Sections       []chunkSection
}

// chunkDecoder decompresses and parses chunks, reusing its buffers and
// decompressors between calls. It is not safe for concurrent use.
type chunkDecoder struct {
	zr    io.ReadCloser
	gzr   *gzip.Reader
	buf   bytes.Buffer
	chunk chunkData
}

// decode parses a region sector (compression byte followed by the payload).
func (d *chunkDecoder) decode(sector []byte) (*chunkData, error) {
	if len(sector) == 0 {
		return nil, errors.New("empty sector")
	}
	payload := sector[1:]
	var src io.Reader
	switch sector[0] {
	case 1:
		if d.gzr == nil {
			gzr, err := gzip.NewReader(bytes.NewReader(payload))
			if err != nil {
				return nil, err
			}
			d.gzr = gzr
		} else if err := d.gzr.Reset(bytes.NewReader(payload)); err != nil {
			return nil, err
		}
		src = d.gzr
	case 2:
		if d.zr == nil {
			zr, err := zlib.NewReader(bytes.NewReader(payload))
			if err != nil {
				return nil, err
			}
			d.zr = zr
		} else if err := d.zr.(zlib.Resetter).Reset(bytes.NewReader(payload), nil); err != nil {
			return nil, err
		}
		src = d.zr
	case 3:
	default:
		return nil, fmt.Errorf("unknown compression type %d", sector[0])
	}

	data := payload
	if src != nil {
		d.buf.Reset()
		if _, err := d.buf.ReadFrom(src); err != nil {
			return nil, err
		}
		data = d.buf.Bytes()
	}

	d.chunk.reset()
	if err := parseChunk(&nbtReader{buf: data}, &d.chunk); err != nil {
		return nil, err
	}
	return &d.chunk, nil
}

func (c *chunkData) reset() {
	c.YPos = 0
	c.MotionBlocking = nil
	c.Sections = c.Sections[:0]
}

// addSection appends a zeroed section, keeping the palette backing array of any
// section previously stored in that slot.
func (c *chunkData) addSection() *chunkSection {
	if len(c.Sections) < cap(c.Sections) {
		c.Sections = c.Sections[:len(c.Sections)+1]
	} else {
		c.Sections = append(c.Sections, chunkSection{})
	}
	s := &c.Sections[len(c.Sections)-1]
	*s = chunkSection{Palette: s.Palette[:0], BiomePalette: s.BiomePalette[:0]}
	return s
}

func parseChunk(r *nbtReader, c *chunkData) error {
	tag, err := r.u8()
	if err != nil {
		return err
	}
	if tag != tagCompound {
		return fmt.Errorf("nbt: root tag is type %d, want compound", tag)
	}
	if _, err := r.str(); err != nil {
		return err
	}
	for {
		tag, name, err := r.field()
		if err != nil || tag == tagEnd {
			return err
		}
		switch {
		case string(name) == "yPos" && isIntTag(tag):
			c.YPos, err = r.int(tag)
		case string(name) == "Heightmaps" && tag == tagCompound:
			err = parseHeightmaps(r, c)
		case string(name) == "sections" && tag == tagList:
			err = parseSections(r, c)
		default:
			err = r.skip(tag, 1)
		}
		if err != nil {
			return err
		}
	}
}

func parseHeightmaps(r *nbtReader, c *chunkData) error {
	for {
		tag, name, err := r.field()
		if err != nil || tag == tagEnd {
			return err
		}
		if string(name) == "MOTION_BLOCKING" && tag == tagLongArray {
			c.MotionBlocking, err = r.longArray()
		} else {
			err = r.skip(tag, 2)
		}
		if err != nil {
			return err
		}
	}
}

func parseSections(r *nbtReader, c *chunkData) error {
	elem, n, err := r.listHeader()
	if err != nil {
		return err
	}
	if elem != tagCompound {
		return r.skipList(elem, n, 2)
	}
	for range n {
		if err := parseSection(r, c.addSection()); err != nil {
			return err
		}
	}
	return nil
}

func parseSection(r *nbtReader, s *chunkSection) error {
	for {
		tag, name, err := r.field()
		if err != nil || tag == tagEnd {
			return err
		}
		switch {
		case string(name) == "Y" && isIntTag(tag):
			s.Y, err = r.int(tag)
		case string(name) == "biomes" && tag == tagCompound:
			err = parseBiomes(r, s)
		case string(name) == "block_states" && tag == tagCompound:
			err = parseBlockStates(r, s)
		default:
			err = r.skip(tag, 3)
		}
		if err != nil {
			return err
		}
	}
}

func parseBlockStates(r *nbtReader, s *chunkSection) error {
	for {
		tag, name, err := r.field()
		if err != nil || tag == tagEnd {
			return err
		}
		switch {
		case string(name) == "palette" && tag == tagList:
			err = parsePalette(r, s)
		case string(name) == "data" && tag == tagLongArray:
			s.Data, err = r.longArray()
		default:
			err = r.skip(tag, 4)
		}
		if err != nil {
			return err
		}
	}
}

func parsePalette(r *nbtReader, s *chunkSection) error {
	s.Palette = s.Palette[:0]
	elem, n, err := r.listHeader()
	if err != nil {
		return err
	}
	if elem != tagCompound {
		return r.skipList(elem, n, 5)
	}
	for range n {
		var blockName []byte
		for {
			tag, name, err := r.field()
			if err != nil {
				return err
			}
			if tag == tagEnd {
				break
			}
			if string(name) == "Name" && tag == tagString {
				blockName, err = r.str()
			} else {
				err = r.skip(tag, 6)
			}
			if err != nil {
				return err
			}
		}
		s.Palette = append(s.Palette, blockName)
	}
	return nil
}

func isIntTag(tag byte) bool {
	return tag == tagByte || tag == tagShort || tag == tagInt || tag == tagLong
}

// nbtReader reads uncompressed big-endian NBT from an in-memory buffer.
// Strings and arrays it returns alias buf.
type nbtReader struct {
	buf []byte
	pos int
}

func (r *nbtReader) next(n int) ([]byte, error) {
	if n < 0 || n > len(r.buf)-r.pos {
		return nil, errNBTTruncated
	}
	b := r.buf[r.pos : r.pos+n]
	r.pos += n
	return b, nil
}

func (r *nbtReader) u8() (byte, error) {
	if r.pos >= len(r.buf) {
		return 0, errNBTTruncated
	}
	b := r.buf[r.pos]
	r.pos++
	return b, nil
}

func (r *nbtReader) i32() (int32, error) {
	b, err := r.next(4)
	if err != nil {
		return 0, err
	}
	return int32(binary.BigEndian.Uint32(b)), nil
}

func (r *nbtReader) str() ([]byte, error) {
	b, err := r.next(2)
	if err != nil {
		return nil, err
	}
	return r.next(int(binary.BigEndian.Uint16(b)))
}

// field reads the type and name of the next entry in a compound. The name is
// not read for tagEnd, which marks the end of the compound.
func (r *nbtReader) field() (byte, []byte, error) {
	tag, err := r.u8()
	if err != nil || tag == tagEnd {
		return tag, nil, err
	}
	name, err := r.str()
	return tag, name, err
}

// int reads a byte, short, int, or long payload as an int.
func (r *nbtReader) int(tag byte) (int, error) {
	switch tag {
	case tagByte:
		b, err := r.u8()
		return int(int8(b)), err
	case tagShort:
		b, err := r.next(2)
		if err != nil {
			return 0, err
		}
		return int(int16(binary.BigEndian.Uint16(b))), nil
	case tagInt:
		v, err := r.i32()
		return int(v), err
	case tagLong:
		b, err := r.next(8)
		if err != nil {
			return 0, err
		}
		return int(int64(binary.BigEndian.Uint64(b))), nil
	}
	return 0, fmt.Errorf("nbt: tag type %d is not an integer", tag)
}

func (r *nbtReader) longArray() (longArray, error) {
	n, err := r.i32()
	if err != nil {
		return nil, err
	}
	b, err := r.next(int(n) * 8)
	return longArray(b), err
}

func (r *nbtReader) listHeader() (elem byte, n int, err error) {
	elem, err = r.u8()
	if err != nil {
		return 0, 0, err
	}
	v, err := r.i32()
	if err != nil {
		return 0, 0, err
	}
	if v < 0 {
		return 0, 0, fmt.Errorf("nbt: negative list length %d", v)
	}
	return elem, int(v), nil
}

// fixedSize returns the payload size of a fixed-width tag, or 0 otherwise.
func fixedSize(tag byte) int {
	switch tag {
	case tagByte:
		return 1
	case tagShort:
		return 2
	case tagInt, tagFloat:
		return 4
	case tagLong, tagDouble:
		return 8
	}
	return 0
}

// skip advances past the payload of a tag whose type byte and name have
// already been read.
func (r *nbtReader) skip(tag byte, depth int) error {
	if depth > maxNBTDepth {
		return errors.New("nbt: exceeded maximum nesting depth")
	}
	if size := fixedSize(tag); size > 0 {
		_, err := r.next(size)
		return err
	}
	switch tag {
	case tagByteArray:
		n, err := r.i32()
		if err != nil {
			return err
		}
		_, err = r.next(int(n))
		return err
	case tagIntArray:
		n, err := r.i32()
		if err != nil {
			return err
		}
		_, err = r.next(int(n) * 4)
		return err
	case tagLongArray:
		_, err := r.longArray()
		return err
	case tagString:
		_, err := r.str()
		return err
	case tagList:
		elem, n, err := r.listHeader()
		if err != nil {
			return err
		}
		return r.skipList(elem, n, depth+1)
	case tagCompound:
		for {
			t, _, err := r.field()
			if err != nil || t == tagEnd {
				return err
			}
			if err := r.skip(t, depth+1); err != nil {
				return err
			}
		}
	}
	return fmt.Errorf("nbt: unknown tag type %d", tag)
}

func (r *nbtReader) skipList(elem byte, n, depth int) error {
	if n == 0 {
		return nil
	}
	if size := fixedSize(elem); size > 0 {
		_, err := r.next(n * size)
		return err
	}
	for range n {
		if err := r.skip(elem, depth); err != nil {
			return err
		}
	}
	return nil
}

// Biomes use a string palette and a packed 4x4x4 grid within each section.
func parseBiomes(r *nbtReader, s *chunkSection) error {
	for {
		tag, name, err := r.field()
		if err != nil || tag == tagEnd {
			return err
		}
		switch {
		case string(name) == "palette" && tag == tagList:
			s.BiomePalette = s.BiomePalette[:0]
			var elem byte
			var n int
			elem, n, err = r.listHeader()
			if err == nil {
				if elem != tagString {
					err = r.skipList(elem, n, 5)
				} else {
					for range n {
						var biome []byte
						biome, err = r.str()
						if err != nil {
							return err
						}
						s.BiomePalette = append(s.BiomePalette, biome)
					}
				}
			}
		case string(name) == "data" && tag == tagLongArray:
			s.BiomeData, err = r.longArray()
		default:
			err = r.skip(tag, 4)
		}
		if err != nil {
			return err
		}
	}
}
