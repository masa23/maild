package mailparser

import (
	"io"
	"mime"
	"strings"

	"golang.org/x/text/encoding/japanese"
)

func DecodeHeader(header string) (string, error) {
	dec := new(mime.WordDecoder)
	dec.CharsetReader = func(charset string, input io.Reader) (io.Reader, error) {
		switch strings.ToLower(charset) {
		case "iso-2022-jp":
			return japanese.ISO2022JP.NewDecoder().Reader(input), nil
		default:
			return input, nil
		}
	}
	// ヘッダーをデコード
	decoded, err := dec.DecodeHeader(header)
	if err != nil {
		return "", err
	}
	return decoded, nil
}
