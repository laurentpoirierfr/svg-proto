package anim

// playerHTML is the minimal player written next to the layers. It loads the
// sidecar, stacks the layers as images in timeline order, and shows the nearest
// keyframe plus the deltas up to the requested frame. Everything below that
// keyframe is hidden rather than merely painted over, so the reset also works
// when the source has transparent pixels.
//
// It is deliberately a file next to the assets rather than a separate runtime:
// the container's promise is that the output is plain SVG plus a small JSON
// description, and a player that needs a build step would hide that.
const playerHTML = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>svgvid player</title>
<style>
  html,body{margin:0;height:100%;background:#111;color:#ddd;font:14px system-ui,sans-serif}
  body{display:flex;flex-direction:column}
  #bar{display:flex;gap:12px;align-items:center;padding:8px 12px;background:#1a1a1a}
  #bar button{background:#333;color:#eee;border:0;border-radius:4px;padding:6px 14px;font:inherit;cursor:pointer}
  #bar input{flex:1;accent-color:#4a9}
  #label{font-variant-numeric:tabular-nums;white-space:nowrap;color:#9a9}
  #stage{position:relative;flex:1;margin:12px auto;width:min(100%,1200px);max-height:100%;align-self:center;aspect-ratio:var(--ar,1)}
  #stage img{position:absolute;inset:0;width:100%;height:100%;display:none}
</style>
</head>
<body>
<div id="bar">
  <button id="play" type="button">Play</button>
  <input id="scrub" type="range" min="0" value="0" step="1">
  <span id="label"></span>
</div>
<div id="stage"></div>
<script>
(async () => {
  const t = await (await fetch("timeline.json")).json();
  const stage = document.getElementById("stage");
  stage.style.setProperty("--ar", t.width + " / " + t.height);
  const layers = t.frames.map((f) => {
    const el = new Image();
    el.src = f.src;
    el.alt = "";
    el.draggable = false;
    stage.appendChild(el);
    return el;
  });
  const scrub = document.getElementById("scrub");
  const label = document.getElementById("label");
  const play = document.getElementById("play");
  scrub.max = t.frames.length - 1;
  let cur = 0, timer = 0;

  function show(i) {
    i = Math.max(0, Math.min(t.frames.length - 1, i | 0));
    const f = t.frames[i];
    const base = f.kind === "delta" ? f.base : i;
    for (let j = 0; j < layers.length; j++) {
      layers[j].style.display = (j === base || (j > base && j <= i)) ? "block" : "none";
    }
    scrub.value = i;
    label.textContent = i + "/" + (t.frames.length - 1) + "  " + f.kind + "  " + f.nodes + " nodes";
    cur = i;
  }
  function tick() {
    show(cur + 1 >= t.frames.length ? 0 : cur + 1);
  }
  play.onclick = () => {
    if (timer) {
      clearInterval(timer);
      timer = 0;
      play.textContent = "Play";
      return;
    }
    play.textContent = "Pause";
    timer = setInterval(tick, 1000 / t.fps);
  };
  scrub.oninput = () => {
    if (timer) {
      clearInterval(timer);
      timer = 0;
      play.textContent = "Play";
    }
    show(+scrub.value);
  };
  show(0);
})();
</script>
</body>
</html>
`
