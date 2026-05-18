Implement a small interpreter for an arithmetic expression language with variables in Go in the current working directory.

Language syntax:
```
program = stmt (";" stmt)*
stmt    = "let" IDENT "=" expr   // declaration
        | expr                   // expression statement (last value is program result)
expr    = term (("+" | "-") term)*
term    = factor (("*" | "/") factor)*
factor  = NUMBER | IDENT | "(" expr ")"
```

Examples:
- `1 + 2` → 3
- `2 * 3 + 4` → 10
- `let x = 5; x * 2` → 10
- `let x = 1; let y = x + 2; y * y` → 9
- `(1 + 2) * (3 + 4)` → 21

Build the pipeline:
- **Lexer**: tokenize input → `[]Token` with kinds `NUMBER`, `IDENT`, `PLUS`, `MINUS`, `STAR`, `SLASH`, `LPAREN`, `RPAREN`, `EQUALS`, `SEMI`, `LET`, `EOF`. Skip whitespace.
- **Parser**: tokens → AST (your choice of node types — interfaces or tagged structs)
- **Evaluator**: AST + environment (`map[string]float64`) → float64

API:
- `Eval(src string) (float64, error)` — convenience that runs lex + parse + eval

Numbers are `float64`. Division by zero is an error. Undefined variable lookup is an error.

Include `go test ./...` covering at least:
1. Simple arithmetic (`1+2`, `3*4-1`)
2. Operator precedence (`2 + 3 * 4` = 14)
3. Parentheses (`(2+3)*4` = 20)
4. Single `let` (`let x = 5; x + 1` = 6)
5. Chained `let` (`let a = 2; let b = a*3; b - 1` = 5)
6. Undefined variable error
7. Division by zero error
8. Lex error / parse error on malformed input

Initialize go.mod yourself. Split files however you like (lexer.go, parser.go, eval.go or different).

Done when `go test ./...` passes.

EXECUTE THIS COMPLETE TASK autonomously. Do not ask for clarifications.
