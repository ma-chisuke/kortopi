# Split a shell command into simple commands ("segments"): one output line per
# segment, its words joined by U+001F, quotes removed.
#
#   jq -r -f segments.jq <<< '"git commit -m \"a; b\" && cat .env"'
#   -> git\u001fcommit\u001f-m\u001fa; b
#   -> cat\u001f.env
#
# Enough to see which programs a command runs and with which arguments; not a
# full shell parser. Here-document bodies are dropped (they are data), and the
# commands inside $(...) and backticks become segments of their own, also
# inside double quotes.

def token_re:
  [
    "'[^']*'?",                        # 'single quoted'
    "\"(?:[^\"\\\\]|\\\\.)*\"?",       # "double quoted"
    "\\$\\(|`",                        # start of a command substitution
    "&&|\\|\\||;;|\\n|[;&|()]",        # command separators
    "[0-9]*(?:<<<|<<-?|>>|>\\||<>|<|>)&?", # redirections
    "(?:[^\\s'\"`;&|()<>$\\\\]|\\\\.|\\$(?!\\())+" # plain word
  ] | join("|");

def is_separator: test("^(?:\\$\\(|`|&&|\\|\\||;;|\\n|[;&|()])$");
def is_redirect: test("^[0-9]*(?:<<<|<<-?|>>|>\\||<>|<|>)&?$");

def unquote:
  if startswith("'") then .[1:] | rtrimstr("'")
  elif startswith("\"") then .[1:] | rtrimstr("\"") | gsub("\\\\(?<c>[\"\\\\$`])"; .c)
  else gsub("\\\\(?<c>.)"; .c)
  end;

# The commands inside $(...) and backticks of a word as written, outside single
# quotes. Words outside quotes never hold them: the tokenizer splits there.
def substitutions:
  gsub("'[^']*'"; "")
  | [ scan("\\$\\(((?:[^()]|\\([^()]*\\))*)\\)"), scan("`([^`]*)`") ]
  | map(.[0]);

def strip_heredocs:
  gsub("<<-?[ \\t]*(?<q>['\"]?)(?<d>[A-Za-z_][A-Za-z0-9_]*)\\k<q>(?<rest>[^\\n]*)\\n(?:[^\\n]*\\n)*?[ \\t]*\\k<d>[ \\t]*(?:\\n|$)";
    "<<HEREDOC\(.rest)\n");

def segments:
  strip_heredocs
  | [ match(token_re; "g") | {s: .string, start: .offset, end: (.offset + .length)} ] as $tokens
  # cur: the words of the current segment; raw: the same words as written.
  | reduce $tokens[] as $t (
      {segs: [], cur: [], raw: [], inner: [], glue: false, last_end: -1};
      if ($t.s | is_separator) then
        .segs += (if .cur == [] then [] else [.cur] end)
        | .inner += [.raw[] | select(test("\\$\\(|`")) | substitutions[]]
        | .cur = [] | .raw = [] | .glue = false
      elif ($t.s | is_redirect) then
        .cur += [$t.s] | .raw += [$t.s] | .glue = false
      elif .glue and $t.start == .last_end then
        .cur[-1] += ($t.s | unquote) | .raw[-1] += $t.s
      else
        .cur += [$t.s | unquote] | .raw += [$t.s] | .glue = true
      end
      | .last_end = $t.end
    )
  | .inner += [.raw[] | select(test("\\$\\(|`")) | substitutions[]]
  | (.segs + (if .cur == [] then [] else [.cur] end))[], (.inner[] | segments);

segments | map(gsub("[\\n\u001f]"; " ")) | join("\u001f")
