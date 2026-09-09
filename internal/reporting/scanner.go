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

type optionalBlock struct {
	Start, ContentStart, ContentEnd, End int
	Placeholders                         []Placeholder
}

type statementScan struct {
	Placeholders   []Placeholder
	OptionalBlocks []optionalBlock
}

func ScanPlaceholders(statement string, mode SQLMode) ([]Placeholder, error) {
	scan, err := scanStatement(statement, mode)
	return scan.Placeholders, err
}

func scanStatement(statement string, mode SQLMode) (statementScan, error) {
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
	scan := statementScan{Placeholders: make([]Placeholder, 0), OptionalBlocks: make([]optionalBlock, 0)}
	var openBlock *optionalBlock
	blockHasSQL := false
	for index := 0; index < len(statement); {
		character := statement[index]
		switch state {
		case normal:
			if index+1 < len(statement) {
				switch statement[index : index+2] {
				case "[[":
					if openBlock != nil {
						return statementScan{}, fmt.Errorf("%w: nested optional SQL blocks are not supported", ErrInvalid)
					}
					openBlock = &optionalBlock{Start: index, ContentStart: index + 2, Placeholders: make([]Placeholder, 0)}
					blockHasSQL = false
					index += 2
					continue
				case "]]":
					if openBlock == nil {
						return statementScan{}, fmt.Errorf("%w: unmatched optional SQL block close", ErrInvalid)
					}
					if !blockHasSQL {
						return statementScan{}, fmt.Errorf("%w: optional SQL block must contain SQL", ErrInvalid)
					}
					openBlock.ContentEnd, openBlock.End = index, index+2
					scan.OptionalBlocks = append(scan.OptionalBlocks, *openBlock)
					openBlock = nil
					index += 2
					continue
				}
			}
			switch character {
			case '\'':
				if openBlock != nil {
					blockHasSQL = true
				}
				state, index = singleString, index+1
			case '"':
				if openBlock != nil {
					blockHasSQL = true
				}
				if mode.ANSIQuotes {
					state = doubleIdentifier
				} else {
					state = doubleString
				}
				index++
			case '`':
				if openBlock != nil {
					blockHasSQL = true
				}
				state, index = backtickIdentifier, index+1
			case '#':
				state, index = lineComment, index+1
			case '-':
				if index+2 < len(statement) && statement[index+1] == '-' && isCommentWhitespace(statement[index+2]) {
					state, index = lineComment, index+3
				} else {
					if openBlock != nil {
						blockHasSQL = true
					}
					index++
				}
			case '/':
				if index+1 < len(statement) && statement[index+1] == '*' {
					state, index = blockComment, index+2
				} else {
					if openBlock != nil {
						blockHasSQL = true
					}
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
					return statementScan{}, fmt.Errorf("%w: invalid placeholder :%s", ErrInvalid, key)
				}
				placeholder := Placeholder{Key: key, Start: index, End: end}
				scan.Placeholders = append(scan.Placeholders, placeholder)
				if openBlock != nil {
					openBlock.Placeholders = append(openBlock.Placeholders, placeholder)
					blockHasSQL = true
				}
				index = end
			default:
				if openBlock != nil && !isCommentWhitespace(character) {
					blockHasSQL = true
				}
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
		return statementScan{}, fmt.Errorf("%w: unterminated SQL lexical context", ErrInvalid)
	}
	if openBlock != nil {
		return statementScan{}, fmt.Errorf("%w: unclosed optional SQL block", ErrInvalid)
	}
	return scan, nil
}

func isIdentifierByte(value byte) bool {
	return value >= 'a' && value <= 'z' || value >= 'A' && value <= 'Z' || value >= '0' && value <= '9' || value == '_'
}

func isCommentWhitespace(value byte) bool { return value <= ' ' || value == 0x7f }
