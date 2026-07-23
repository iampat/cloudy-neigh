# cloudy-neigh logo

![cloudy-neigh logo](logo.png)

Generated with Google's **`gemini-3.1-flash-image`** (Nano Banana) model,
1:1 aspect ratio, on 2026-07-23.

## Prompt

> Playful flat-illustration logo for "cloudy-neigh", a cloud-native search
> engine. A friendly chubby horse whose body is made of white fluffy cloud
> puffs, joyfully leaping across a soft sky-blue circle, tiny clouds around it.
> Cute but clean: simple shapes, thick outlines, limited palette of sky blue,
> white and deep navy. No gradients, no text, sticker-like modern mascot style,
> centered on white with generous margins, suitable as a GitHub project logo.

## Regenerating

Requires a `GEMINI_API_KEY` environment variable. Note that generation is
non-deterministic — each run produces a new variation, not this exact image.

```sh
curl -s "https://generativelanguage.googleapis.com/v1beta/models/gemini-3.1-flash-image:generateContent" \
  -H "x-goog-api-key: $GEMINI_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "contents": [{"parts": [{"text": "Playful flat-illustration logo for \"cloudy-neigh\", a cloud-native search engine. A friendly chubby horse whose body is made of white fluffy cloud puffs, joyfully leaping across a soft sky-blue circle, tiny clouds around it. Cute but clean: simple shapes, thick outlines, limited palette of sky blue, white and deep navy. No gradients, no text, sticker-like modern mascot style, centered on white with generous margins, suitable as a GitHub project logo."}]}],
    "generationConfig": {
      "responseModalities": ["TEXT", "IMAGE"],
      "imageConfig": {"aspectRatio": "1:1"}
    }
  }' \
| python3 -c 'import base64, json, sys
parts = json.load(sys.stdin)["candidates"][0]["content"]["parts"]
data = next(p["inlineData"]["data"] for p in parts if "inlineData" in p)
open("logo.png", "wb").write(base64.b64decode(data))'
```
