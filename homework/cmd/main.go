package main

import (
	"bytes"
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"strings"
	"unicode"
	"unicode/utf8"
)

type Options struct {
	From      string
	To        string
	Offset    uint64
	Limit     uint64
	BlockSize uint64
	Conv      []string
}

func validatedConvs(convs string) ([]string, error) {
	if len(convs) == 0 {
		return make([]string, 0), nil
	}

	convValues := strings.Split(convs, ",")
	convMap := map[string]struct{}{
		"lower_case":  {},
		"upper_case":  {},
		"trim_spaces": {},
	}
	hasLower, hasUpper := false, false

	for _, val := range convValues {
		if _, ok := convMap[val]; !ok {
			return nil, fmt.Errorf("invalid argument of -conv")
		} else if val == "lower_case" {
			hasLower = true
		} else if val == "upper_case" {
			hasUpper = true
		}
	}
	if hasLower && hasUpper {
		return nil, fmt.Errorf("invalid argument of -conv")
	}

	return convValues, nil
}

func ParseFlags() (*Options, error) {
	var opts Options

	flag.StringVar(&opts.From, "from", "", "file to read. by default - stdin")
	flag.StringVar(&opts.To, "to", "", "file to write. by default - stdout")
	flag.Uint64Var(&opts.Offset, "offset", 0, "the number of bytes, that must be skipped")
	flag.Uint64Var(&opts.Limit, "limit", math.MaxInt, "maximum number of bytes read")
	flag.Uint64Var(&opts.BlockSize, "block-size", 1024, "size of one block in bytes when reading and writing")
	var convs string
	flag.StringVar(&convs, "conv", "", "one or more of the possible transformations on the text")
	// todo: parse and validate all flags

	flag.Parse()

	convValues, err := validatedConvs(convs)
	if err != nil {
		return nil, err
	}
	opts.Conv = convValues

	return &opts, nil
}

type MapReader struct {
	file    io.Reader
	toUpper bool
	safe    []byte
	buffer  []byte
	pos     int
}

func (mr *MapReader) Read(p []byte) (n int, err error) {
	if len(mr.safe) != 0 {
		if len(p) < len(mr.safe) {
			copy(p, mr.safe[:len(p)])
			mr.safe = mr.safe[len(p):]
			return len(p), nil
		} else {
			copy(p[:len(mr.safe)], mr.safe)
			prevLen := len(mr.safe)
			mr.safe = make([]byte, 0)
			return prevLen, nil
		}
	}

	buffer := make([]byte, len(p))
	n, err = mr.file.Read(buffer)
	if err != nil {
		return n, err
	}

	right := 0
	mr.buffer = append(mr.buffer, bytes.TrimRight(buffer, "\x00")...)
	for i := 0; i < len(mr.buffer); {
		r, size := utf8.DecodeRune(mr.buffer[i:])
		if r == utf8.RuneError {
			mr.buffer = mr.buffer[right:]
			return mr.Read(p)
		}

		if mr.toUpper {
			mr.safe = append(mr.safe, []byte(strings.ToUpper(string(r)))...)
		} else {
			mr.safe = append(mr.safe, []byte(strings.ToLower(string(r)))...)
		}

		right += size
		i += size
	}

	mr.buffer = mr.buffer[right:]

	return mr.Read(p)
}

type TrimReader struct {
	file    io.Reader
	buffer  []byte
	safe    []byte
	skipped bool
}

func (tr *TrimReader) Read(p []byte) (n int, err error) {
	if len(tr.safe) != 0 {
		if len(p) < len(tr.safe) {
			copy(p, tr.safe[:len(p)])
			tr.safe = tr.safe[len(p):]
			return len(p), nil
		} else {
			copy(p[:len(tr.safe)], tr.safe)
			prevLen := len(tr.safe)
			tr.safe = make([]byte, 0)
			return prevLen, nil
		}
	}

	buffer := make([]byte, len(p))
	n, err = tr.file.Read(buffer)
	if err != nil {
		return n, err
	}

	tr.buffer = append(tr.buffer, bytes.TrimRight(buffer, "\x00")...)
	right := 0
	for i := 0; i < len(tr.buffer); {
		r, size := utf8.DecodeRune(tr.buffer[i:])
		if r == utf8.RuneError {
			tr.buffer = tr.buffer[right:]
			return tr.Read(p)
		}

		if unicode.IsSpace(r) {
			i += size
			continue
		}

		if !tr.skipped && !unicode.IsSpace(r) {
			tr.skipped = true
			tr.safe = append(tr.safe, tr.buffer[i:i+size]...)
			right = i + size
			i += size
			continue
		}

		if !unicode.IsSpace(r) {
			tr.safe = append(tr.safe, tr.buffer[right:i+size]...)
			right = i + size
			i += size
			continue
		}
	}

	tr.buffer = tr.buffer[right:]

	return tr.Read(p)
}

func CreateReader(opts *Options) (io.Reader, error) {
	var file io.Reader
	var err error

	if opts.From == "" {
		file = os.Stdin
	} else {
		file, err = os.Open(opts.From)
		if err != nil {
			return nil, err
		}
	}

	n, err := io.CopyN(io.Discard, file, int64(opts.Offset))
	if err != nil {
		return nil, err
	}
	if n < int64(opts.Offset) {
		return nil, fmt.Errorf("error while skipping bytes")
	}

	file = io.LimitReader(file, int64(opts.Limit))

	if len(opts.Conv) != 0 {
		for _, val := range opts.Conv {
			switch val {
			case "lower_case":
				file = &MapReader{file: file, toUpper: false}
			case "upper_case":
				file = &MapReader{file: file, toUpper: true}
			case "trim_spaces":
				file = &TrimReader{file: file}
			}
		}
	}

	return file, nil
}

func createWriter(to string) (io.Writer, error) {
	if to == "" {
		return os.Stdout, nil
	}

	_, err := os.Stat(to)
	if !os.IsNotExist(err) {
		return nil, err
	}

	return os.Create(to)
}

func main() {
	opts, err := ParseFlags()
	if err != nil {
		_, _ = fmt.Fprintln(os.Stderr, "can not parse flags:", err)
		os.Exit(1)
	}

	reader, err := CreateReader(opts)
	if err != nil {
		_, _ = fmt.Fprintln(os.Stderr, "can not create reader:", err)
		os.Exit(1)
	}

	writer, err := createWriter(opts.To)
	if err != nil {
		_, _ = fmt.Fprintln(os.Stderr, "can not create writer:", err)
		os.Exit(1)
	}

	_, err = io.CopyBuffer(struct{ io.Writer }{writer}, reader, make([]byte, opts.BlockSize))
	if err != nil {
		_, _ = fmt.Fprintln(os.Stderr, "error while copping:", err)
		os.Exit(1)
	}
}
