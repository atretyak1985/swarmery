#!/usr/bin/env bash
# Regenerates the site's short clips, episode posters and screenshots.
#
#   bash scripts/site/media.sh
#
# Sources (not committed, kept next to the repo by the author):
#   video/final/swarmery-<name>.mp4   voiced episodes and the promo, 1920x1080
#   video/raw/swarmery-ep6-knowledge.mp4   episode 6, not voiced yet
#   video/screens/<section>/*.png     2880x1620 screenshots (fictional data)
# Outputs:
#   site/assets/clips/<name>.mp4|.jpg   muted 1280x720 loops, ~0.5-1.5 MB each
#   site/assets/posters/<episode>.jpg   poster frames for the full episodes
#   site/assets/img/<shot>{,-sm,-lg}.webp   1600 / 800 / 2400 px wide
# Needs ffmpeg, bc and python3 with Pillow.
set -euo pipefail
cd "$(git rev-parse --show-toplevel)"

src() { if [ "$1" = ep6-knowledge ]; then echo "video/raw/swarmery-$1.mp4"; else echo "video/final/swarmery-$1.mp4"; fi; }

mkdir -p site/assets/clips site/assets/posters site/assets/img

# name  source  start(s)  duration(s)
while read -r name ep ss dur; do
  [ -z "$name" ] && continue
  in=$(src "$ep"); out=site/assets/clips/$name
  ffmpeg -nostdin -v error -y -ss "$ss" -t "$dur" -i "$in" -an -vf "scale=1280:-2,fps=30" \
    -c:v libx264 -preset medium -crf 27 -pix_fmt yuv420p -profile:v high -movflags +faststart "$out.mp4"
  ffmpeg -nostdin -v error -y -ss "$(echo "$ss+2" | bc)" -i "$in" -frames:v 1 -vf scale=1280:-2 -q:v 4 "$out.jpg"
  echo "clip $name"
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
  ffmpeg -nostdin -v error -y -ss "$2" -i "$(src "$1")" -frames:v 1 -vf scale=1280:-2 -q:v 3 "site/assets/posters/$1.jpg"
done

python3 - <<'PY'
import glob, os
from PIL import Image
for p in sorted(glob.glob('video/screens/*/*.png')):
    n = os.path.basename(p)[:-4]
    if 'serena' in n:   # third-party dashboard, kept off the site
        continue
    im = Image.open(p).convert('RGB')
    for w, suf, q in ((1600, '', 82), (800, '-sm', 82), (2400, '-lg', 80)):
        im.resize((w, round(im.height * w / im.width)), Image.LANCZOS).save(f'site/assets/img/{n}{suf}.webp', 'WEBP', quality=q, method=6)
print('screenshots done')
PY
