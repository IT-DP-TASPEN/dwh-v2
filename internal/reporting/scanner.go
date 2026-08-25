package reporting

import (
	"fmt"
	"regexp"
	"strings"
)

var parameterKeyPattern = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

type SQLMode struct {
	ANSIQuotes         bool
	NoBackslashEscapes bool
}

func ParseSQLMode(value string) SQLMode {
	var mode SQLMode
	for _, item := range strings.Split(value, ",") {
		switch strings.TrimSpace(item) {
		case "ANSI_QUOTES":
			mode.ANSIQuotes = true
		case "NO_BACKSLASH_ESCAPES":
			mode.NoBackslashEscapes = true
		}
	}
	return mode
}

type Placeholder struct {
	Key        string
	Start, End int
}

func ScanPlaceholders(statement string, mode SQLMode) ([]Placeholder, error) {
	const (
		normal = iota
		singleString
		doubleString
		doubleIdentifier
		backtickIdentifier
		lineComment
		blockComment
	)
	state := normal
	placeholders := make([]Placeholder, 0)
	for index := 0; index < len(statement); {
		character := statement[index]
		switch state {
		case normal:
			switch character {
			case '\'':
				state, index = singleString, index+1
			case '"':
				if mode.ANSIQuotes {
					state = doubleIdentifier
				} else {
					state = doubleString
				}
				index++
			case '`':
				state, index = backtickIdentifier, index+1
			case '#':
				state, index = lineComment, index+1
			case '-':
				if index+2 < len(statement) && statement[index+1] == '-' && isCommentWhitespace(statement[index+2]) {
					state, index = lineComment, index+3
				} else {
					index++
				}
			case '/':
				if index+1 < len(statement) && statement[index+1] == '*' {
					state, index = blockComment, index+2
				} else {
					index++
				}
			case ':':
				end := index + 1
				for end < len(statement) && isIdentifierByte(statement[end]) {
					end++
				}
				if end == index+1 {
					index++
					continue
				}
				key := statement[index+1 : end]
				if !parameterKeyPattern.MatchString(key) {
					return nil, fmt.Errorf("%w: invalid placeholder :%s", ErrInvalid, key)
				}
				placeholders = append(placeholders, Placeholder{Key: key, Start: index, End: end})
				index = end
			default:
				index++
			}
		case singleString, doubleString:
			quote := byte('\'')
			if state == doubleString {
				quote = '"'
			}
			if character == quote {
				if index+1 < len(statement) && statement[index+1] == quote {
					index += 2
					continue
				}
				state, index = normal, index+1
			} else if character == '\\' && !mode.NoBackslashEscapes && index+1 < len(statement) {
				index += 2
			} else {
				index++
			}
		case doubleIdentifier, backtickIdentifier:
			quote := byte('"')
			if state == backtickIdentifier {
				quote = '`'
			}
			if character == quote {
				if index+1 < len(statement) && statement[index+1] == quote {
					index += 2
					continue
				}
				state, index = normal, index+1
			} else {
				index++
			}
		case lineComment:
			if character == '\n' || character == '\r' {
				state = normal
			}
			index++
		case blockComment:
			if character == '*' && index+1 < len(statement) && statement[index+1] == '/' {
				state, index = normal, index+2
			} else {
				index++
			}
		}
	}
	if state != normal && state != lineComment {
		return nil, fmt.Errorf("%w: unterminated SQL lexical context", ErrInvalid)
	}
	return placeholders, nil
}

func isIdentifierByte(value byte) bool {
	return value >= 'a' && value <= 'z' || value >= 'A' && value <= 'Z' || value >= '0' && value <= '9' || value == '_'
}

func isCommentWhitespace(value byte) bool { return value <= ' ' || value == 0x7f }
