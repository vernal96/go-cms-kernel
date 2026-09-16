package cacheentry

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"sort"
)

const (
	version      byte = 1
	maxTagCount       = 1 << 16
	maxTagLength      = 1 << 20
)

var magic = [4]byte{'C', 'M', 'S', 'C'}

type Token [32]byte

type Entry struct {
	ExpiresAt int64
	Tags      map[string]Token
	Value     []byte
}

func Encode(entry Entry) ([]byte, error) {
	if len(entry.Tags) > maxTagCount {
		return nil, errors.New("cache entry has too many tags")
	}

	keys := make([]string, 0, len(entry.Tags))
	for tag := range entry.Tags {
		if tag == "" {
			return nil, errors.New("cache entry tag is empty")
		}
		if len(tag) > maxTagLength {
			return nil, errors.New("cache entry tag is too long")
		}
		keys = append(keys, tag)
	}
	sort.Strings(keys)

	var buffer bytes.Buffer
	buffer.Write(magic[:])
	buffer.WriteByte(version)
	if err := binary.Write(
		&buffer,
		binary.BigEndian,
		entry.ExpiresAt,
	); err != nil {
		return nil, err
	}
	if err := binary.Write(
		&buffer,
		binary.BigEndian,
		uint32(len(keys)),
	); err != nil {
		return nil, err
	}
	for _, tag := range keys {
		if err := binary.Write(
			&buffer,
			binary.BigEndian,
			uint32(len(tag)),
		); err != nil {
			return nil, err
		}
		buffer.WriteString(tag)
		token := entry.Tags[tag]
		buffer.Write(token[:])
	}
	if err := binary.Write(
		&buffer,
		binary.BigEndian,
		uint64(len(entry.Value)),
	); err != nil {
		return nil, err
	}
	buffer.Write(entry.Value)
	return buffer.Bytes(), nil
}

func Decode(raw []byte) (Entry, error) {
	reader := bytes.NewReader(raw)
	var encodedMagic [4]byte
	if _, err := io.ReadFull(reader, encodedMagic[:]); err != nil {
		return Entry{}, errors.New("cache entry header is truncated")
	}
	if encodedMagic != magic {
		return Entry{}, errors.New("cache entry magic is invalid")
	}
	encodedVersion, err := reader.ReadByte()
	if err != nil {
		return Entry{}, errors.New("cache entry version is missing")
	}
	if encodedVersion != version {
		return Entry{}, fmt.Errorf(
			"cache entry version %d is unsupported",
			encodedVersion,
		)
	}

	var result Entry
	if err := binary.Read(
		reader,
		binary.BigEndian,
		&result.ExpiresAt,
	); err != nil {
		return Entry{}, errors.New("cache entry expiration is truncated")
	}
	var tagCount uint32
	if err := binary.Read(reader, binary.BigEndian, &tagCount); err != nil {
		return Entry{}, errors.New("cache entry tag count is truncated")
	}
	if tagCount > maxTagCount {
		return Entry{}, errors.New("cache entry has too many tags")
	}
	result.Tags = make(map[string]Token, tagCount)
	for range tagCount {
		var tagLength uint32
		if err := binary.Read(
			reader,
			binary.BigEndian,
			&tagLength,
		); err != nil {
			return Entry{}, errors.New("cache entry tag length is truncated")
		}
		if tagLength == 0 || tagLength > maxTagLength {
			return Entry{}, errors.New("cache entry tag length is invalid")
		}
		tagBytes := make([]byte, tagLength)
		if _, err := io.ReadFull(reader, tagBytes); err != nil {
			return Entry{}, errors.New("cache entry tag is truncated")
		}
		tag := string(tagBytes)
		if _, exists := result.Tags[tag]; exists {
			return Entry{}, errors.New("cache entry contains duplicate tag")
		}
		var token Token
		if _, err := io.ReadFull(reader, token[:]); err != nil {
			return Entry{}, errors.New("cache entry tag token is truncated")
		}
		result.Tags[tag] = token
	}

	var valueLength uint64
	if err := binary.Read(
		reader,
		binary.BigEndian,
		&valueLength,
	); err != nil {
		return Entry{}, errors.New("cache entry value length is truncated")
	}
	if valueLength > uint64(reader.Len()) {
		return Entry{}, errors.New("cache entry value is truncated")
	}
	if valueLength != uint64(reader.Len()) {
		return Entry{}, errors.New("cache entry contains trailing data")
	}
	result.Value = make([]byte, int(valueLength))
	if _, err := io.ReadFull(reader, result.Value); err != nil {
		return Entry{}, errors.New("cache entry value is truncated")
	}
	return result, nil
}
