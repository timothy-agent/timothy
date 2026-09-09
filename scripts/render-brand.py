"""Regenerate assets/brand/ from the pixel grid in web/public/favicon.svg.

Run in Docker (no host Python/Pillow):

  docker run --rm -v "$PWD":/w -w /w python:3.13-slim sh -c \
    "pip install -q pillow && python3 scripts/render-brand.py"
"""

from PIL import Image

GROUND_A = "#080c09"
GROUND_B = "#17241a"
GREEN = "#00e654"

# 8x8 grid, row-major. "A"/"B" alternate ground tones, "T" is the mark.
GRID = [
    ["A", "B", "A", "B", "A", "B", "A", "B"],
    ["B", "A", "B", "A", "B", "A", "B", "A"],
    ["B", "A", "T", "T", "T", "T", "A", "B"],
    ["A", "B", "A", "T", "T", "A", "B", "A"],
    ["B", "A", "B", "T", "T", "B", "A", "B"],
    ["A", "B", "A", "T", "T", "A", "B", "A"],
    ["B", "A", "B", "A", "B", "A", "B", "A"],
    ["A", "B", "A", "B", "A", "B", "A", "B"],
]

COLORS = {"A": GROUND_A, "B": GROUND_B, "T": GREEN}


def svg_grid(include_ground: bool) -> str:
    rects = []
    for row in range(8):
        for col in range(8):
            cell = GRID[row][col]
            if not include_ground and cell != "T":
                continue
            color = COLORS[cell]
            rects.append(
                f'<rect x="{col * 6}" y="{row * 6}" width="6" height="6" fill="{color}"/>'
            )
    return (
        '<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 48 48" '
        f'shape-rendering="crispEdges">{"".join(rects)}</svg>'
    )


def render_png(size: int, include_ground: bool) -> Image.Image:
    img = Image.new("RGBA", (size, size), (0, 0, 0, 0))
    px = img.load()
    bounds = [round(i * size / 8) for i in range(9)]
    for row in range(8):
        y0, y1 = bounds[row], bounds[row + 1]
        for col in range(8):
            cell = GRID[row][col]
            if not include_ground and cell != "T":
                continue
            x0, x1 = bounds[col], bounds[col + 1]
            color = COLORS[cell]
            r = int(color[1:3], 16)
            g = int(color[3:5], 16)
            b = int(color[5:7], 16)
            for y in range(y0, y1):
                for x in range(x0, x1):
                    px[x, y] = (r, g, b, 255)
    return img


def main() -> None:
    mark_svg = svg_grid(include_ground=True)
    for name in ("timothy-mark.svg", "timothy-mark-square.svg"):
        with open(f"assets/brand/{name}", "w") as f:
            f.write(mark_svg)

    with open("assets/brand/timothy-glyph.svg", "w") as f:
        f.write(svg_grid(include_ground=False))

    for size in (16, 32, 64, 128, 256, 512, 1024):
        render_png(size, include_ground=True).save(f"assets/brand/timothy-mark-{size}.png")

    render_png(512, include_ground=False).save("assets/brand/timothy-glyph-512.png")
    render_png(180, include_ground=True).save("assets/brand/apple-touch-icon.png")

    ico_sizes = (16, 32, 48)
    ico_images = [render_png(s, include_ground=True) for s in ico_sizes]
    # Base image must be the largest size: Pillow's ICO writer skips any
    # requested size bigger than the base image's own dimensions.
    ico_images[-1].save(
        "assets/brand/favicon.ico",
        format="ICO",
        sizes=[(s, s) for s in ico_sizes],
        append_images=ico_images[:-1],
    )


if __name__ == "__main__":
    main()
