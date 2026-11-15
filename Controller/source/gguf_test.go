package main

import (
	"bytes"
	"encoding/binary"
	"testing"
)

func putU64(b *bytes.Buffer, v uint64)        { _ = binary.Write(b, binary.LittleEndian, v) }
func putU32(b *bytes.Buffer, v uint32)        { _ = binary.Write(b, binary.LittleEndian, v) }
func putGGUFString(b *bytes.Buffer, s string) { putU64(b, uint64(len(s))); b.WriteString(s) }

func TestParseGGUFHeaderReadsArchitectureAndQuantization(t *testing.T) {
	var b bytes.Buffer
	b.WriteString("GGUF")
	putU32(&b, 3)
	putU64(&b, 0)
	putU64(&b, 3)
	putGGUFString(&b, "general.architecture")
	putU32(&b, 8)
	putGGUFString(&b, "qwen3vl")
	putGGUFString(&b, "general.name")
	putU32(&b, 8)
	putGGUFString(&b, "Qwen3 VL test")
	putGGUFString(&b, "general.file_type")
	putU32(&b, 4)
	putU32(&b, 15)
	info, err := parseGGUFHeader(bytes.NewReader(b.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	if info.Architecture != "qwen3vl" || info.Name != "Qwen3 VL test" || info.FileType != 15 {
		t.Fatalf("unexpected info: %+v", info)
	}
	if !recognizedArchitecture(info.Architecture) || quantizationName(info.FileType) != "Q4_K_M" {
		t.Fatalf("recognition/quantization failed: %+v", info)
	}
}

func TestParseGGUFHeaderRejectsBadMagic(t *testing.T) {
	if _, err := parseGGUFHeader(bytes.NewReader([]byte("NOPE"))); err == nil {
		t.Fatal("expected invalid GGUF error")
	}
}
