package main

import (
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

var ErrInvalidConv = fmt.Errorf("invalid argument of -conv")

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
			return nil, fmt.Errorf("%w: unknown conv %s", ErrInvalidConv, val)
		} else if val == "lower_case" {
			hasLower = true
		} else if val == "upper_case" {
			hasUpper = true
		}
	}
	if hasLower && hasUpper {
		return nil, fmt.Errorf("%w: lower and upper case cannot be used at the same time", ErrInvalidConv)
	}

	return convValues, nil
}

func ParseFlags() (*Options, error) {
	var opts Options
	var convs string

	flag.StringVar(&opts.From, "from", "", "file to read. by default - stdin")
	flag.StringVar(&opts.To, "to", "", "file to write. by default - stdout")
	flag.Uint64Var(&opts.Offset, "offset", 0, "the number of bytes, that must be skipped")
	flag.Uint64Var(&opts.Limit, "limit", math.MaxInt, "maximum number of bytes read")
	flag.Uint64Var(&opts.BlockSize, "block-size", 1024, "size of one block in bytes when reading and writing")
	flag.StringVar(&convs, "conv", "", "one or more of the possible transformations on the text")

	flag.Parse()

	convValues, err := validatedConvs(convs)
	if err != nil {
		return nil, err
	}
	opts.Conv = convValues

	return &opts, nil
}

func copyFromChecked(dst []byte, src []byte) ([]byte, int) {
	length := min(len(dst), len(src))
	copy(dst[:length], src[:length])
	src = src[length:]
	return src, length
}

type CaseReader struct {
	reader  io.Reader
	toUpper bool
	mapped  []byte
	buffer  []byte
}

func (cr *CaseReader) Read(p []byte) (n int, err error) {
	if len(cr.mapped) != 0 {
		cr.mapped, n = copyFromChecked(p, cr.mapped)
		return n, nil
	}

	buffer := make([]byte, len(p))
	n, err = cr.reader.Read(buffer)
	if err != nil {
		return n, err
	}
	cr.buffer = append(cr.buffer, buffer[:n]...)

	var i, runeSize int
	var r rune
	for i = 0; i < len(cr.buffer); i += runeSize {
		r, runeSize = utf8.DecodeRune(cr.buffer[i:])
		if r == utf8.RuneError {
			break
		}

		if cr.toUpper {
			cr.mapped = append(cr.mapped, []byte(strings.ToUpper(string(r)))...)
		} else {
			cr.mapped = append(cr.mapped, []byte(strings.ToLower(string(r)))...)
		}
	}

	cr.buffer = cr.buffer[i:]
	return cr.Read(p)
}

type TrimReader struct {
	reader        io.Reader
	buffer        []byte
	trimmed       []byte
	skippedSpaces bool
}

func (tr *TrimReader) Read(p []byte) (n int, err error) {
	if len(tr.trimmed) != 0 {
		tr.trimmed, n = copyFromChecked(p, tr.trimmed)
		return n, nil
	}

	buffer := make([]byte, len(p))
	n, err = tr.reader.Read(buffer)
	if err != nil {
		return n, err
	}
	tr.buffer = append(tr.buffer, buffer[:n]...)

	var runeSize, firstSpacePos int
	var r rune
	for i := 0; i < len(tr.buffer); i += runeSize {
		r, runeSize = utf8.DecodeRune(tr.buffer[i:])
		if r == utf8.RuneError {
			break
		}

		if unicode.IsSpace(r) {
			continue
		}

		if tr.skippedSpaces {
			tr.trimmed = append(tr.trimmed, tr.buffer[firstSpacePos:i+runeSize]...)
		} else {
			tr.trimmed = append(tr.trimmed, tr.buffer[i:i+runeSize]...)
			tr.skippedSpaces = true
		}
		firstSpacePos = i + runeSize
	}

	tr.buffer = tr.buffer[firstSpacePos:]
	return tr.Read(p)
}

func CreateReader(opts *Options) (io.Reader, error) {
	var reader io.Reader
	var err error

	if opts.From == "" {
		reader = os.Stdin
	} else {
		reader, err = os.Open(opts.From)
		if err != nil {
			return nil, err
		}
	}

	n, err := io.CopyN(io.Discard, reader, int64(opts.Offset))
	if err != nil {
		return nil, err
	}
	if n < int64(opts.Offset) {
		return nil, fmt.Errorf("error while skipping bytes")
	}

	reader = io.LimitReader(reader, int64(opts.Limit))

	if len(opts.Conv) != 0 {
		for _, val := range opts.Conv {
			switch val {
			case "lower_case":
				reader = &CaseReader{reader: reader, toUpper: false}
			case "upper_case":
				reader = &CaseReader{reader: reader, toUpper: true}
			case "trim_spaces":
				reader = &TrimReader{reader: reader}
			}
		}
	}

	return reader, nil
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
