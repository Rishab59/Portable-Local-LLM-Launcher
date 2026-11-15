package main

import (
	"encoding/binary"
	"errors"
	"io"
	"os"
)

type GGUFInfo struct {
	Architecture     string `json:"architecture,omitempty"`
	Name             string `json:"modelName,omitempty"`
	FileType         int64  `json:"fileType,omitempty"`
	FileTypeKnown    bool   `json:"fileTypeKnown"`
	Quantization     string `json:"quantization,omitempty"`
	Recognized       bool   `json:"architectureRecognized"`
	MetadataReadable bool   `json:"metadataReadable"`
}

func readGGUFInfo(path string) GGUFInfo {
	f, err := os.Open(path)
	if err != nil {
		return GGUFInfo{}
	}
	defer f.Close()
	info, err := parseGGUFHeader(f)
	if err != nil {
		return GGUFInfo{MetadataReadable: false}
	}
	info.Recognized = recognizedArchitecture(info.Architecture)
	info.MetadataReadable = true
	if info.FileTypeKnown {
		info.Quantization = quantizationName(info.FileType)
	} else {
		info.Quantization = "unknown"
	}
	return info
}

func parseGGUFHeader(r io.ReadSeeker) (GGUFInfo, error) {
	var info GGUFInfo
	magic := make([]byte, 4)
	if _, err := io.ReadFull(r, magic); err != nil || string(magic) != "GGUF" {
		return info, errors.New("not a GGUF file")
	}
	var version uint32
	var tensorCount, metadataCount uint64
	if binary.Read(r, binary.LittleEndian, &version) != nil || version < 1 || version > 3 {
		return info, errors.New("unsupported GGUF version")
	}
	if binary.Read(r, binary.LittleEndian, &tensorCount) != nil || binary.Read(r, binary.LittleEndian, &metadataCount) != nil {
		return info, errors.New("invalid GGUF header")
	}
	if metadataCount > 100000 {
		return info, errors.New("unreasonable GGUF metadata count")
	}
	_ = tensorCount
	fileTypeSeen := false
	for i := uint64(0); i < metadataCount; i++ {
		key, err := readGGUFString(r)
		if err != nil {
			return info, err
		}
		var typ uint32
		if err := binary.Read(r, binary.LittleEndian, &typ); err != nil {
			return info, err
		}
		capture := key == "general.architecture" || key == "general.name" || key == "general.file_type"
		value, err := readGGUFValue(r, typ, capture)
		if err != nil {
			return info, err
		}
		if capture {
			switch key {
			case "general.architecture":
				if v, ok := value.(string); ok {
					info.Architecture = v
				}
			case "general.name":
				if v, ok := value.(string); ok {
					info.Name = v
				}
			case "general.file_type":
				if v, ok := value.(int64); ok {
					info.FileType = v
					info.FileTypeKnown = true
					fileTypeSeen = true
				}
			}
		}
		if info.Architecture != "" && info.Name != "" && fileTypeSeen {
			break
		}
	}
	return info, nil
}

func readGGUFString(r io.ReadSeeker) (string, error) {
	var n uint64
	if err := binary.Read(r, binary.LittleEndian, &n); err != nil {
		return "", err
	}
	if n > 64*1024*1024 {
		return "", errors.New("unreasonable GGUF string length")
	}
	b := make([]byte, n)
	if _, err := io.ReadFull(r, b); err != nil {
		return "", err
	}
	return string(b), nil
}

func readGGUFValue(r io.ReadSeeker, typ uint32, capture bool) (any, error) {
	switch typ {
	case 0:
		var v uint8
		err := binary.Read(r, binary.LittleEndian, &v)
		return int64(v), err
	case 1:
		var v int8
		err := binary.Read(r, binary.LittleEndian, &v)
		return int64(v), err
	case 2:
		var v uint16
		err := binary.Read(r, binary.LittleEndian, &v)
		return int64(v), err
	case 3:
		var v int16
		err := binary.Read(r, binary.LittleEndian, &v)
		return int64(v), err
	case 4:
		var v uint32
		err := binary.Read(r, binary.LittleEndian, &v)
		return int64(v), err
	case 5:
		var v int32
		err := binary.Read(r, binary.LittleEndian, &v)
		return int64(v), err
	case 6:
		var v float32
		err := binary.Read(r, binary.LittleEndian, &v)
		return nil, err
	case 7:
		var v uint8
		err := binary.Read(r, binary.LittleEndian, &v)
		return nil, err
	case 8:
		if capture {
			return readGGUFString(r)
		}
		var n uint64
		if err := binary.Read(r, binary.LittleEndian, &n); err != nil {
			return nil, err
		}
		if n > 1<<40 {
			return nil, errors.New("unreasonable GGUF string length")
		}
		_, err := r.Seek(int64(n), io.SeekCurrent)
		return nil, err
	case 9:
		var elementType uint32
		var count uint64
		if err := binary.Read(r, binary.LittleEndian, &elementType); err != nil {
			return nil, err
		}
		if err := binary.Read(r, binary.LittleEndian, &count); err != nil {
			return nil, err
		}
		if count > 100000000 {
			return nil, errors.New("unreasonable GGUF array length")
		}
		for i := uint64(0); i < count; i++ {
			if _, err := readGGUFValue(r, elementType, false); err != nil {
				return nil, err
			}
		}
		return nil, nil
	case 10:
		var v uint64
		err := binary.Read(r, binary.LittleEndian, &v)
		return int64(v), err
	case 11:
		var v int64
		err := binary.Read(r, binary.LittleEndian, &v)
		return v, err
	case 12:
		var v float64
		err := binary.Read(r, binary.LittleEndian, &v)
		return nil, err
	default:
		return nil, errors.New("unknown GGUF metadata value type")
	}
}

func recognizedArchitecture(arch string) bool {
	switch arch {
	case "llama", "qwen2", "qwen3", "qwen3vl", "qwen2vl", "mistral", "mixtral", "gemma", "gemma2", "gemma3", "phi2", "phi3", "phi4", "deepseek2", "deepseek3", "command-r", "cohere2", "starcoder2", "granite", "internlm2", "stablelm", "falcon", "bloom", "gptneox", "mpt", "baichuan", "yi", "orion", "dbrx", "jamba", "mamba", "rwkv6", "plamo", "exaone", "minicpm", "olmo", "olmo2", "nemotron", "arctic", "smollm", "smollm2", "t5", "bert", "nomic-bert", "clip", "whisper":
		return true
	default:
		return false
	}
}

func quantizationName(fileType int64) string {
	switch fileType {
	case 0:
		return "F32"
	case 1:
		return "F16"
	case 2:
		return "Q4_0"
	case 3:
		return "Q4_1"
	case 7:
		return "Q8_0"
	case 8:
		return "Q5_0"
	case 9:
		return "Q5_1"
	case 10:
		return "Q2_K"
	case 11:
		return "Q3_K_S"
	case 12:
		return "Q3_K_M"
	case 13:
		return "Q3_K_L"
	case 14:
		return "Q4_K_S"
	case 15:
		return "Q4_K_M"
	case 16:
		return "Q5_K_S"
	case 17:
		return "Q5_K_M"
	case 18:
		return "Q6_K"
	default:
		return "unknown"
	}
}
