#!/usr/bin/env python3
"""Blur sensitive regions in docs/screenshots/ before public release.

Regions are (left, top, right, bottom) in pixels on the actual PNG size
(~1340×1250 px). Run once after replacing screenshots.
Verified: no readable text in sensitive zones after blurring.
"""

import os
from PIL import Image, ImageFilter

SCREENSHOTS = os.path.join(os.path.dirname(__file__), '..', 'docs', 'screenshots')

BLUR_RADIUS = 22
BLUR_PASSES = 4   # heavy — text must not be legible at 200% zoom

# (left, top, right, bottom)
REGIONS: dict[str, list[tuple[int, int, int, int]]] = {

    # Overview: cost/errors badges, active session title, recently-completed
    # session titles, entire right panel (errors + cost + projects)
    'overview.png': [
        (730, 88,  1135, 185),  # COST + ERRORS metric badges
        (195, 198, 670,  320),  # active session card
        (195, 298, 558,  645),  # recently-completed session title column
        (835, 212, 1135, 740),  # right panel: errors-today, cost-by-model, projects
    ],

    # Sessions: title column only (project names are already anonymised as
    # ProjectA/B/C/D so no need to blur them; model/branch/duration are fine)
    'sessions.png': [
        (290, 55, 660, 820),
    ],

    # Session detail — Approvals diff: title banner + full diff view
    'session-approvals.png': [
        (110, 20,  1336, 80),
        (110, 80,  1336, 825),
    ],

    # Session detail — Timeline: title banner + timeline entries
    'session-timeline.png': [
        (110, 20,  1342, 80),
        (110, 80,  1342, 825),
    ],

    # Approvals queue: all card content
    'approvals.png': [
        (110, 50, 1335, 825),
    ],

    # Hooks: project-specific (non-swarmery) hook entries that reveal internal
    # tooling names (bash scripts). Swarmery-managed hooks stay visible.
    'system-hooks.png': [
        (215, 278, 820, 345),   # SessionStart — project hook command + path
        (215, 613, 440, 648),   # PreToolUse  — guard-paths.sh
        (215, 800, 460, 845),   # PostToolUse — prettier write project hook
        (215, 985, 450, 1038),  # Stop        — completion-summary.sh
    ],
}


def blur_region(img: Image.Image, box: tuple[int, int, int, int]) -> Image.Image:
    l, t, r, b = max(0, box[0]), max(0, box[1]), min(img.width, box[2]), min(img.height, box[3])
    if r <= l or b <= t:
        return img
    crop = img.crop((l, t, r, b))
    for _ in range(BLUR_PASSES):
        crop = crop.filter(ImageFilter.GaussianBlur(BLUR_RADIUS))
    img.paste(crop, (l, t))
    return img


def process(filename: str, regions: list[tuple[int, int, int, int]]) -> None:
    path = os.path.join(SCREENSHOTS, filename)
    if not os.path.exists(path):
        print(f'  skip (not found): {filename}')
        return
    img = Image.open(path).convert('RGBA')
    print(f'  {filename}  {img.size}  ({len(regions)} region(s))')
    for box in regions:
        img = blur_region(img, box)
    img.convert('RGB').save(path, 'PNG', optimize=True)
    print(f'    saved.')


if __name__ == '__main__':
    print('Blurring sensitive regions in docs/screenshots/...')
    for fname, boxes in REGIONS.items():
        process(fname, boxes)
    print('Done.')
