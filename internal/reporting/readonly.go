package reporting

import "strings"

// ValidateReadOnlyStatement is the shared policy for templates, options and all
// execution paths. Quoting and comments follow the parameter scanner's SQL mode.
// Executable MySQL comments are rejected because their contents are SQL.
func ValidateReadOnlyStatement(statement string, mode SQLMode) error {
	scan, err := scanStatement(statement, mode)
	if err != nil || scan.ExecutableComment || len(scan.Tokens) == 0 {
		return ErrInvalid
	}
	tokens := scan.Tokens
	if tokens[len(tokens)-1] == ";" {
		tokens = tokens[:len(tokens)-1]
	}
	depth := 0
	for i, token := range tokens {
		switch token {
		case ";", ":=", "INTO":
			return ErrInvalid
		case "(":
			depth++
		case ")":
			depth--
			if depth < 0 {
				return ErrInvalid
			}
		}
		// These clauses are unsafe even inside a subquery or CTE.
		if token == "FOR" && i+1 < len(tokens) && (tokens[i+1] == "UPDATE" || tokens[i+1] == "SHARE") {
			return ErrInvalid
		}
		if token == "LOCK" && i+3 < len(tokens) && strings.Join(tokens[i:i+4], " ") == "LOCK IN SHARE MODE" {
			return ErrInvalid
		}
		// Function identifiers may be quoted. Decode only function names here;
		// their contents never participate in keyword/clause detection.
		functionName := token
		if name, exists := scan.QuotedNames[i]; exists {
			functionName = name
		}
		// Named locks survive transaction rollback and contaminate pooled sessions.
		if (functionName == "GET_LOCK" || functionName == "RELEASE_LOCK" || functionName == "RELEASE_ALL_LOCKS") && i+1 < len(tokens) && tokens[i+1] == "(" {
			return ErrInvalid
		}
		if functionName == "LAST_INSERT_ID" && i+2 < len(tokens) && tokens[i+1] == "(" && tokens[i+2] != ")" {
			return ErrInvalid
		}
	}
	if depth != 0 || !readOnlyQuery(tokens) {
		return ErrInvalid
	}
	return nil
}

func readOnlyQuery(tokens []string) bool {
	if len(tokens) == 0 {
		return false
	}
	if tokens[0] == "SELECT" {
		return true
	}
	if tokens[0] != "WITH" {
		return false
	}
	i := 1
	if i < len(tokens) && tokens[i] == "RECURSIVE" {
		i++
	}
	for i < len(tokens) {
		// A CTE starts with an identifier, optionally followed by column names.
		if tokens[i] != "<quoted>" && !parameterKeyPattern.MatchString(strings.ToLower(tokens[i])) {
			return false
		}
		i++
		if i < len(tokens) && tokens[i] == "(" {
			end := closingParen(tokens, i)
			if end < 0 {
				return false
			}
			i = end + 1
		}
		if i+1 >= len(tokens) || tokens[i] != "AS" || tokens[i+1] != "(" {
			return false
		}
		i += 1
		end := closingParen(tokens, i)
		if end < 0 || !readOnlyQuery(tokens[i+1:end]) {
			return false
		}
		i = end + 1
		if i < len(tokens) && tokens[i] == "," {
			i++
			continue
		}
		return i < len(tokens) && tokens[i] == "SELECT"
	}
	return false
}

func closingParen(tokens []string, start int) int {
	depth := 0
	for i := start; i < len(tokens); i++ {
		if tokens[i] == "(" {
			depth++
		}
		if tokens[i] == ")" {
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}
