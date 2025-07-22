package mailparser

import (
	"errors"
	"strings"
)

var (
	ErrInvalidEmailFormat = errors.New("invalid email address format")
)

func ParseAddressList(s string) ([]string, error) {
	var addresses []string
	var quoted bool
	var escape bool
	var comment bool
	var depth int
	var buf strings.Builder

	for _, r := range s {
		switch {
		case escape:
			buf.WriteRune(r)
			escape = false
		case r == '\\':
			escape = true
		case r == '"':
			if !comment {
				quoted = !quoted
			}
			buf.WriteRune(r)
		case r == '(' && !quoted:
			comment = true
			depth = 1
		case r == ')' && comment:
			depth--
			if depth == 0 {
				comment = false
			}
		case comment:
			continue
		case r == ',' && !quoted:
			part := strings.TrimSpace(buf.String())
			if part != "" {
				addresses = append(addresses, part)
			}
			buf.Reset()
		default:
			if comment {
				continue
			}
			buf.WriteRune(r)
		}
	}

	// 最後の要素を追加
	if trimmed := strings.TrimSpace(buf.String()); trimmed != "" {
		addresses = append(addresses, trimmed)
	}

	if len(addresses) == 0 {
		return nil, ErrInvalidEmailFormat
	}

	return addresses, nil
}

// Fromのヘッダからメールアドレスを取り出す
func ParseAddress(s string) (name, mbox, host string) {
	var address string
	var quoted bool
	var escape bool
	var afeeld bool
	var comment bool
	var depth int
	var start, end int

	var buf strings.Builder

	for i, r := range s {
		switch {
		case escape:
			escape = false
		case r == '\\':
			escape = true
		case r == '"' && !afeeld && !comment:
			quoted = !quoted
		case r == '(' && !quoted:
			comment = true
			depth = 1
		case r == ')' && comment:
			depth--
			if depth == 0 {
				comment = false
			}
		case comment:
			continue
		case r == '<' && !quoted && !comment:
			afeeld = true
			start = i
		case r == '>' && !quoted && !comment:
			afeeld = false
			end = i
		}
		if !comment {
			buf.WriteRune(r)
		}
	}

	clean := buf.String()

	if start < end {
		address = clean[start+1 : end]
	} else {
		address = clean
	}
	address = strings.TrimSpace(address)
	mbox, host = parseHostDomain(address)

	// 名前の抽出
	name = strings.TrimSpace(clean[:start])

	return name, mbox, host
}

func parseHostDomain(address string) (mbox, host string) {
	// @の位置を探す
	at := strings.Index(address, "@")
	if at < 0 {
		// @が見つからない場合はmboxとしてaddress全体を返す
		return strings.TrimSpace(address), ""
	}

	// メールボックスとホスト名を分割
	mbox = strings.TrimSpace(address[:at])
	host = strings.TrimSpace(address[at+1:])

	return mbox, host
}
