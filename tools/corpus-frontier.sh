#!/bin/bash
# Compare the line path and the curve path at a common node budget.
#
# The point of a node budget is that it binds. Applying -max-nodes to one mode
# and not the other measures two different things: the constrained mode gets
# simplified until it fits, the other is measured raw, and the difference is
# read as a property of the mode. At a 20000 node ceiling the line path was
# pushed to 1 px tolerance on logo/mark while the curve path sat at 966 nodes
# untouched, which is how a 22x win was published as 11x and a 48x win as 1.1x.
#
# So: one budget, both modes, and the effective tolerance reported next to the
# requested one. A row where the two differ is a row where the budget did the
# work, and it is marked rather than silently averaged into a ratio.
#
# Usage: corpus-frontier.sh [node budget]      default 4000

set -u

BUDGET=${1:-4000}
K=${K:-16}
TOL=${TOL:-0.25}
CURVE_TOL=${CURVE_TOL:-0.4}
CURVE_SIMPLIFY=${CURVE_SIMPLIFY:-1.5}
BG=${BG:-white}
ORACLE=${ORACLE:-inkscape}
CORPUS=${CORPUS:-testdata/corpus}
OUT=$(mktemp -d)
trap 'rm -rf "$OUT"' EXIT

svgimg=${SVGIMG:-bin/svgimg}
svgstat=${SVGSTAT:-bin/svgstat}
for bin in "$svgimg" "$svgstat"; do
  [ -x "$bin" ] || { echo "$bin missing: run make build first" >&2; exit 1; }
done

# One trace, one score. Field extraction is by field name rather than by
# position: the summary has a "nodes" line and a truncation note that also
# mentions nodes, and matching the first line containing a word picks up
# whichever comes first.
measure() {
  local png=$1 mode=$2 svg=$3
  local log=$OUT/log
  if [ "$mode" = curve ]; then
    "$svgimg" -in "$png" -mode trace -k "$K" -tol "$TOL" -max-nodes "$BUDGET" \
      -curves -curve-tol "$CURVE_TOL" -curve-simplify "$CURVE_SIMPLIFY" -out "$svg" >"$log" 2>&1
  else
    "$svgimg" -in "$png" -mode trace -k "$K" -tol "$TOL" -max-nodes "$BUDGET" \
      -out "$svg" >"$log" 2>&1
  fi || return 1
  NODES=$(awk '$1=="nodes"{print $2; exit}' "$log")
  EFFTOL=$(awk '$1=="tolerance"{print $2; exit}' "$log")
  [ -n "$NODES" ] || return 1
  local row
  row=$("$svgstat" "$svg" --ref "$png" --oracle "$ORACLE" -bg "$BG" \
    -format row -category "$mode" -name x 2>/dev/null)
  [ -n "$row" ] || return 1
  # The row is empty, category, name, then one field per column, then a
  # trailing separator, so the score is the second to last field. Taking the
  # last one yields the empty field after the final bar.
  SCORE=$(printf '%s' "$row" | awk -F'|' '{gsub(/^ +| +$/,"",$(NF-1)); print $(NF-1)}')
  [ -n "$SCORE" ] || return 1
  return 0
}

printf '# Frontiere lignes/courbes a un budget de noeuds commun\n\n'
printf 'Budget %s noeuds, k=%s, -tol %s, -curve-tol %s, -curve-simplify %s.\n' \
  "$BUDGET" "$K" "$TOL" "$CURVE_TOL" "$CURVE_SIMPLIFY"
printf 'Les deux modes sont brides au meme plafond. La colonne "tol" est la\n'
printf 'tolerance effective, celle que la boucle de budget a reellement utilisee :\n'
printf 'si elle differe de %s, c est le plafond qui a travaille, pas le mode.\n\n' "$TOL"
printf '| cas | mode | noeuds | budget | tol | score |\n'
printf '|---|---|--:|--:|--:|--:|\n'

tl=0; tc=0; rows=0
for png in "$CORPUS"/*/*.png; do
  [ -e "$png" ] || continue
  case=$(basename "$(dirname "$png")")/$(basename "$png" .png)
  ln=""; lc=""
  if measure "$png" line "$OUT/l.svg"; then ln=$NODES; ls_=$SCORE; lt=$EFFTOL; fi
  if measure "$png" curve "$OUT/c.svg"; then lc=$NODES; cs=$SCORE; ct=$EFFTOL; fi
  [ -n "$ln" ] && [ -n "$lc" ] || { printf '| %s | echec de mesure, voir ci-dessous | | | | |\n' "$case"; continue; }
  printf '| %s | lignes | %s | %s | %s | %s |\n' "$case" "$ln" "$BUDGET" "$lt" "$ls_"
  printf '| %s | courbes | %s | %s | %s | %s |\n' "$case" "$lc" "$BUDGET" "$ct" "$cs"
  tl=$((tl + ln)); tc=$((tc + lc)); rows=$((rows + 1))
done

printf '\n'
awk -v a="$tl" -v b="$tc" -v n="$rows" -v bud="$BUDGET" 'BEGIN{
  if (n == 0) { print "aucun cas mesure"; exit 1 }
  printf "- total lignes %d, total courbes %d, sur %d cas, plafond %d chacun\n", a, b, n, n*bud
  printf "- rapport de noeuds %s\n", (b > 0 ? sprintf("%.1fx", a/b) : "n/a")
  printf "- usage du plafond : lignes %.0f%%, courbes %.0f%%\n", a/(n*bud)*100, b/(n*bud)*100
}'
