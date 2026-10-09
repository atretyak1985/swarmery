# Ukrainian overlay for the site (built under site/uk/). Empty until the
# translation lands: every key below falls back to English, and
# `python3 scripts/site/build.py --strict` lists what is still missing.
#
# UI      — the same keys as locales/en.py UI, translated.
# CONTENT — translations of data.py copy; structure (slugs, shots, episodes,
#           durations, URLs) stays in data.py only:
#   "features":      {slug: {"name", "kicker", "title", "lede", "menu",
#                            "beats": [(k, h, p, [bullets]), …]  # one per data.py beat
#                            "chapters": ["title", …]}}          # one per data.py chapter
#   "promo":         {"title", "text", "chapters": ["title", …]}
#   "shot_captions": {shot_id: "caption"}
#   "principles":    {principle_id: (label, title, text)}
#   "cp_pages":      {page_name: "text"}       # dashboard page names stay English (D6)
#   "posts":         {post_url: "tag"}         # keyed by the data.py POSTS URL (p[3])
#
# A data.py entry with no translation falls back to English (content.<map>.<id>
# under --strict); an entry naming no data.py slug, id, page, shot or post URL
# is reported as uk:unknown:content.<map>.<id>. UI keys in en.ESCAPED_KEYS are
# plain text: no markup, no entities.

UI = {}

CONTENT = {}

# post dates: genitive month names, "7 жовтня 2026"
MONTHS = ["січня", "лютого", "березня", "квітня", "травня", "червня",
          "липня", "серпня", "вересня", "жовтня", "листопада", "грудня"]
DATE = "{d} {mon} {y}"
