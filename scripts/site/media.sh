#!/usr/bin/env bash
# Regenerates the site's short clips, episode posters and screenshots.
#
#   bash scripts/site/media.sh
#
# Sources (not committed, kept next to the repo by the author):
#   video/final/swarmery-<name>.mp4   voiced episodes and the promo, 1920x1080
#   video/raw/swarmery-<name>.mp4     fallback when an episode has no voiced cut yet
#   video/screens/<section>/*.png     2880x1620 screenshots (fictional data)
#   video/light/{final,raw,screens}/  the same set rendered in the light theme
# Outputs (dark, and the light twins one folder down in light/):
#   site/assets/clips/[light/]<name>.mp4|.jpg   muted 1280x720 loops, ~0.5-1.5 MB each
#   site/assets/posters/[light/]<episode>.jpg   poster frames for the full episodes
#   site/assets/img/[light/]<shot>{,-sm,-lg}.webp   1600 / 800 / 2400 px wide
# The site swaps to the light twins when the reader picks the light theme
# (site.js themeMedia); the full light episodes go to docs/video/light/.
# Needs ffmpeg, bc and python3 with Pillow.
set -euo pipefail
cd "$(git rev-parse --show-toplevel)"

for theme in dark light; do
if [ "$theme" = dark ]; then V=video; T=""; else V=video/light; T=light/; fi
[ -d "$V/final" ] || [ -d "$V/raw" ] || { echo "skip $theme: no $V/final or $V/raw"; continue; }

src() { if [ -f "$V/final/swarmery-$1.mp4" ]; then echo "$V/final/swarmery-$1.mp4"; else echo "$V/raw/swarmery-$1.mp4"; fi; }

mkdir -p "site/assets/clips/$T" "site/assets/posters/$T" "site/assets/img/$T"

# name  source  start(s)  duration(s)
while read -r name ep ss dur; do
  [ -z "$name" ] && continue
  in=$(src "$ep"); out=site/assets/clips/$T$name
  ffmpeg -nostdin -v error -y -ss "$ss" -t "$dur" -i "$in" -an -vf "scale=1280:-2,fps=30" \
    -c:v libx264 -preset medium -crf 27 -pix_fmt yuv420p -profile:v high -movflags +faststart "$out.mp4"
  ffmpeg -nostdin -v error -y -ss "$(echo "$ss+2" | bc)" -i "$in" -frames:v 1 -vf scale=1280:-2 -q:v 4 "$out.jpg"
  echo "clip $T$name"
done <<'EOF'
hero promo 20.5 24
promo-fleet promo 49.5 10
plan-interview ep1-planning 14.5 18
plan-lands ep1-planning 45.5 14
plan-revision ep1-planning 90.5 13
inbox-queue ep2-inbox 11.5 16
inbox-questions ep2-inbox 27.5 12
inbox-lesson ep2-inbox 41.5 12
inbox-rewrite ep2-inbox 65.5 13
health-overview ep3-health 13.5 13
health-friction ep3-health 29.5 12
health-cost ep3-health 61.5 12
sessions-list ep4-sessions 11.5 16
sessions-timeline ep4-sessions 39.5 13
sessions-diffs ep4-sessions 50.5 12
learning-lessons ep5-learning 13.5 15
learning-classifier ep5-learning 41.5 12
learning-honesty ep5-learning 51.5 17
knowledge-memory ep6-knowledge 12.5 17
knowledge-arch ep6-knowledge 31.5 20
knowledge-graph ep6-knowledge 61.5 10
EOF

for p in "promo 22" "ep1-planning 16" "ep2-inbox 16" "ep3-health 16" "ep4-sessions 16" "ep5-learning 16" "ep6-knowledge 41"; do
  set -- $p
  ffmpeg -nostdin -v error -y -ss "$2" -i "$(src "$1")" -frames:v 1 -vf scale=1280:-2 -q:v 3 "site/assets/posters/$T$1.jpg"
done

V="$V" T="$T" python3 - <<'PY'
import glob, os
from PIL import Image
V, T = os.environ['V'], os.environ['T']
for p in sorted(glob.glob(f'{V}/screens/*/*.png')):
    n = os.path.basename(p)[:-4]
    if 'serena' in n:   # third-party dashboard, kept off the site
        continue
    im = Image.open(p).convert('RGB')
    for w, suf, q in ((1600, '', 82), (800, '-sm', 82), (2400, '-lg', 80)):
        im.resize((w, round(im.height * w / im.width)), Image.LANCZOS).save(f'site/assets/img/{T}{n}{suf}.webp', 'WEBP', quality=q, method=6)
print(f'screenshots done ({T or "dark"})')
PY
done
