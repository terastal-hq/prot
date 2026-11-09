/* prot landing page. No framework, no build. Everything the terminals print comes from data.js (real CLI output). */
(() => {
  "use strict";

  const $ = (s, r = document) => r.querySelector(s);
  const $$ = (s, r = document) => [...r.querySelectorAll(s)];
  const calm = matchMedia("(prefers-reduced-motion: reduce)").matches;
  const clamp = (v, a, b) => Math.min(b, Math.max(a, v));
  const lerp = (a, b, t) => a + (b - a) * t;
  const wait = (ms) => new Promise((r) => setTimeout(r, ms));
  const OUT = window.PROT_OUT || {};

  /** Run fn once when el is mostly on screen. */
  function whenSeen(el, fn, threshold = 0.5) {
    const io = new IntersectionObserver((es) => {
      if (es.some((e) => e.isIntersecting)) {
        io.disconnect();
        fn();
      }
    }, { threshold });
    io.observe(el);
  }

  /* ---------------------------------------------------------------- entrances as you scroll */

  // [selector, direction, first delay in seconds, step between siblings]. Direction names are styled in style.css.
  const ENTRANCES = [
    [".cap", "up", 0.1, 0],
    [".logo", "drop", 0.05, 0],
    [".sub", "left", 0.35, 0],
    [".install", "left", 0.5, 0],
    [".dl", "pop", 1.0, 0.1],
    [".garg", "pop", 0.7, 0],
    [".top .btn", "down", 0.6, 0.1],
    [".tape-a", "tape-l", 0, 0], [".tape-b", "tape-r", 0, 0], [".tape-c", "tape-l", 0, 0], [".tape-d", "tape-r", 0, 0],
    [".tape-e", "tape-l", 0, 0],
    [".soup .tag", "pop", 0, 0.045],
    [".big", "right", 0.5, 0],
    [".crash-card", "left", 0, 0],
    [".crash-log", "right", 0.25, 0],
    [".kill-wrap", "down", 0.6, 0],
    [".desk .w-add", "left", 0, 0],
    [".desk .w-ls", "right", 0.25, 0],
    [".desk .w-status", "down", 0.5, 0],
    [".tun-card", "left", 0, 0],
    [".rack", "right", 0.2, 0],
    [".cable-svg", "fade", 0.7, 0],
    [".plug", "fade", 0.7, 0],
    [".arrow-plug", "fade", 1.4, 0],
    [".notepad", "up", 0.1, 0],
    [".hud", "down", 0, 0],
    [".arms li", "pop", 0.5, 0.06],
    [".locks li", "drop", 1.3, 0.12],
    [".hud-face", "pop", 0.4, 0],
    [".hud-count", "pop", 0.3, 0],
    [".foot .btn", "up", 0.2, 0.08],
  ];
  if (!calm) {
    const io = new IntersectionObserver((es) => {
      es.forEach((e) => {
        if (!e.isIntersecting) return;
        e.target.classList.add("in");
        io.unobserve(e.target);
      });
    }, { threshold: 0.12, rootMargin: "0px 0px -6% 0px" });
    ENTRANCES.forEach(([sel, dir, d0, step]) => {
      $$(sel).forEach((el, i) => {
        el.classList.add("rv");
        el.dataset.rv = dir;
        el.style.setProperty("--d", (d0 + i * step).toFixed(2) + "s");
        io.observe(el);
      });
    });
    // sections with something to do besides sliding in
    const seen = $("#names");
    whenSeen(seen, () => seen.classList.add("seen"), 0.2);
    const count = $(".hud-count b");
    whenSeen($("#hud"), () => {
      const t0 = performance.now() + 500;
      const tick = (now) => {
        const p = clamp((now - t0) / 700, 0, 1);
        count.textContent = Math.round(p * 12);
        if (p < 1) requestAnimationFrame(tick);
      };
      count.textContent = "0";
      requestAnimationFrame(tick);
    }, 0.3);
  }

  /* ---------------------------------------------------------------- hazard tape: stripes slide as you scroll */

  if (!calm) {
    const tapes = $$(".tape").map((el, i) => ({ el, dir: i % 2 ? -1 : 1 }));
    const PERIOD = 33.941; // one stripe period along x at 45 degrees: 24px * sqrt(2); the tile repeats seamlessly
    let queued = false;
    const slide = () => {
      queued = false;
      tapes.forEach(({ el, dir }) => {
        // the stripes travel with the scroll position, and neighbouring tapes go opposite ways
        el.style.setProperty("--tp", ((scrollY * 0.9 * dir) % PERIOD).toFixed(2));
      });
    };
    addEventListener("scroll", () => {
      if (!queued) {
        queued = true;
        requestAnimationFrame(slide);
      }
    }, { passive: true });
    slide();
  }

  /* ---------------------------------------------------------------- install box */

  const INSTALL = "curl -fsSL https://prot.adityagupta.dev/install | bash";
  {
    const pre = $("#cmd");
    const copy = $("#copy");
    pre.textContent = INSTALL;
    let reset = 0;
    const flash = (label) => {
      copy.classList.add("done");
      copy.title = label;
      copy.setAttribute("aria-label", label);
      clearTimeout(reset);
      reset = setTimeout(() => {
        copy.classList.remove("done");
        copy.title = "Copy";
        copy.setAttribute("aria-label", "Copy the install command");
      }, 2000);
    };
    copy.addEventListener("click", async () => {
      try {
        await navigator.clipboard.writeText(INSTALL);
        flash("Copied");
      } catch {
        const r = document.createRange();
        r.selectNodeContents(pre);
        getSelection().removeAllRanges();
        getSelection().addRange(r);
        flash("Selected: press Ctrl+C");
      }
    });
  }

  /* ---------------------------------------------------------------- the exploded app */

  {
    const plane = $("#plane");
    const box = $("#stage-box");
    const col = $("#stage-col");
    const hero = $("#top");
    const REST = { rx: 9, ry: -19, rz: 2 };
    const cur = { ...REST, e: 0 };
    const want = { ...REST };
    let intro = calm ? 1 : 0;
    let introStart = 0;
    let visible = true;
    let raf = 0;

    const fit = () => {
      const s = clamp(Math.min((col.clientWidth - 10) / 600, (innerHeight * 0.9) / 900), 0.5, 1.05);
      box.style.setProperty("--s", s.toFixed(3));
    };

    const backOut = (t) => {
      const c = 1.9;
      return 1 + (c + 1) * Math.pow(t - 1, 3) + c * Math.pow(t - 1, 2);
    };

    const draw = (now) => {
      raf = 0;
      if (!introStart) introStart = now + 450;
      if (!calm && intro < 1) intro = clamp(backOut(clamp((now - introStart) / 1500, 0, 1)), 0, 1.25);
      const sc = clamp(scrollY / Math.max(1, hero.offsetHeight), 0, 1);
      const e = intro * (1 + sc * 0.6);
      cur.rx = lerp(cur.rx, want.rx, 0.08);
      cur.ry = lerp(cur.ry, want.ry, 0.08);
      cur.rz = lerp(cur.rz, want.rz, 0.08);
      plane.style.setProperty("--e", e.toFixed(3));
      plane.style.setProperty("--rx", cur.rx.toFixed(2) + "deg");
      plane.style.setProperty("--ry", cur.ry.toFixed(2) + "deg");
      plane.style.setProperty("--rz", cur.rz.toFixed(2) + "deg");
      const settling = intro < 1 || Math.abs(cur.ry - want.ry) > 0.02 || Math.abs(cur.rx - want.rx) > 0.02;
      if (visible && (settling || !calm)) request();
    };
    const request = () => {
      if (!raf) raf = requestAnimationFrame(draw);
    };

    fit();
    addEventListener("resize", () => {
      fit();
      request();
    });
    if (calm) {
      intro = 1;
      plane.style.setProperty("--e", "1");
    } else {
      // Tilt, where the browser allows it without asking (Android Chrome). iOS Safari sends nothing until the user grants
      // permission, which is not requested here, so on an iPhone this simply never fires. The phone's angle, relative to how it is held when the page opens, turns the stack. The reference drifts
      // slowly toward the current angle, so the view settles back to rest if you change how you hold the phone.
      let tilting = false;
      if (matchMedia("(pointer: coarse)").matches && "DeviceOrientationEvent" in window) {
        let base = null;
        const RANGE = 18; // degrees of phone tilt for the full turn of the stack
        addEventListener("deviceorientation", (ev) => {
          if (ev.gamma == null || ev.beta == null || innerWidth > innerHeight) return;
          tilting = true;
          if (!base) base = { g: ev.gamma, b: ev.beta };
          base.g += (ev.gamma - base.g) * 0.004;
          base.b += (ev.beta - base.b) * 0.004;
          const x = clamp((ev.gamma - base.g) / RANGE, -1, 1);
          const y = clamp((ev.beta - base.b) / RANGE, -1, 1);
          want.ry = REST.ry + x * 12;
          want.rx = REST.rx - y * 9;
          want.rz = REST.rz + x * 2;
          request();
        }, { passive: true });
      }
      addEventListener("pointermove", (ev) => {
        if (tilting && ev.pointerType !== "mouse") return; // a finger on the screen should not fight the sensors
        const x = ev.clientX / innerWidth - 0.5;
        const y = ev.clientY / innerHeight - 0.5;
        want.ry = REST.ry + x * 12;
        want.rx = REST.rx - y * 9;
        want.rz = REST.rz + x * 2;
        request();
      }, { passive: true });
      addEventListener("scroll", request, { passive: true });
      new IntersectionObserver((es) => {
        visible = es[0].isIntersecting;
        if (visible) request();
      }).observe(hero);
    }
    request();
  }

  /* ---------------------------------------------------------------- ports become names */

  {
    const root = $("#names");
    const field = $("#soup-field");
    const btn = $("#soup-btn");
    const face = $(".big-face", btn);
    const tags = $$(".tag", field);
    // chaos: x and y as fractions of the field, rotation in degrees, size in px. Crowded on purpose.
    const CHAOS = [
      [0.03, 0.06, -8, 92], [0.27, 0.46, 5, 56], [0.42, 0.2, -3, 104], [0.72, 0.4, 9, 40], [0.08, 0.64, 6, 66],
      [0.4, 0.66, -7, 78], [0.62, 0.8, 3, 44], [0.84, 0.12, -11, 70], [0.24, 0.12, 13, 38], [0.86, 0.62, -5, 58],
      // decoys
      [0.16, 0.34, -4, 48], [0.46, 0.34, 7, 34], [0.74, 0.6, -9, 42], [0.34, 0.86, 4, 60], [0.04, 0.88, -12, 36],
      [0.56, 0.52, 10, 30],
    ];
    const TILT = [-1.1, 0.8, -0.4, 1.3, -0.9, 0.5, -1.4, 0.7, -0.2, 1.1];
    const NAMES = tags.map((t) => t.dataset.name || "");

    tags.forEach((t) => {
      t.innerHTML = `<span class="pre">localhost</span><span class="txt">:${t.dataset.port}</span><span class="suf">:8080</span>`;
    });

    let named = false;
    const layout = () => {
      const W = field.clientWidth;
      const narrow = W < 640;
      tags.forEach((t, i) => {
        const [fx, fy, r, fs] = CHAOS[i];
        let x;
        let y;
        let rot;
        let sc = 1;
        if (t.classList.contains("decoy") && named) {
          // decoys leave through the nearest edge
          x = fx < 0.5 ? -W * 0.15 : W * 1.05;
          y = fy * field.clientHeight;
          rot = r * 3;
          sc = 0.3;
        } else if (!named) {
          const k = narrow ? 0.7 : 1;
          t.style.setProperty("--fs", fs * k + "px");
          x = clamp(fx * W, 0, Math.max(0, W - 150 * k * (fs / 50)));
          y = fy * field.clientHeight;
          rot = r;
        } else if (narrow) {
          x = W * (0.05 + (i % 2) * 0.03);
          y = 12 + i * 56;
          rot = TILT[i];
        } else {
          const col = i < 5 ? 0 : 1;
          x = W * (col ? 0.5 : 0.05) + (i % 3) * 14;
          y = 24 + (i % 5) * 92 + (col ? 46 : 0);
          rot = TILT[i];
        }
        t.style.setProperty("--x", x.toFixed(1) + "px");
        t.style.setProperty("--y", y.toFixed(1) + "px");
        t.style.setProperty("--r", rot + "deg");
        t.style.setProperty("--sc", sc);
      });
      field.style.height = named && narrow ? 12 + NAMES.filter(Boolean).length * 56 + 20 + "px" : "";
    };

    const GLYPHS = "0123456789abcdef#:./";
    const scramble = (el, to, ms) => {
      if (calm) {
        el.textContent = to;
        return;
      }
      const t0 = performance.now();
      const step = (now) => {
        const p = clamp((now - t0) / ms, 0, 1);
        let s = "";
        for (let i = 0; i < to.length; i++) {
          s += i < p * to.length ? to[i] : GLYPHS[(Math.random() * GLYPHS.length) | 0];
        }
        el.textContent = p >= 1 ? to : s;
        if (p < 1) requestAnimationFrame(step);
      };
      requestAnimationFrame(step);
    };

    const setNamed = (flag) => {
      named = flag;
      root.classList.toggle("named", flag);
      btn.setAttribute("aria-pressed", flag);
      face.textContent = flag ? face.dataset.b : face.dataset.a;
      tags.forEach((t, i) => {
        const txt = $(".txt", t);
        if (t.classList.contains("decoy")) {
          t.classList.toggle("gone", flag);
          return;
        }
        const go = () => {
          t.classList.toggle("named", flag);
          scramble(txt, flag ? NAMES[i] + ".localhost" : ":" + t.dataset.port, 560);
        };
        calm ? go() : setTimeout(go, i * 80);
      });
      // layout after the classes flip, so widths are the new ones
      requestAnimationFrame(layout);
    };

    layout();
    addEventListener("resize", layout);
    btn.addEventListener("click", () => setNamed(!named));
    if (calm) setNamed(true);
    else whenSeen(field, () => setTimeout(() => !named && setNamed(true), 1100), 0.65);
  }

  /* ---------------------------------------------------------------- crash and restart */

  {
    const card = $("#crash-card");
    const imgs = $$("img", card);
    const out = $("#crash-out");
    const kill = $("#kill");
    const lines = (OUT.logs_api || "").split("\n");
    const show = (s) => imgs.forEach((i) => i.classList.toggle("on", i.dataset.s === s));
    const esc = (s) => s.replace(/&/g, "&amp;").replace(/</g, "&lt;");
    const fmt = (l) => {
      const k = /exited unexpectedly/.test(l) ? "bad" : /Restart policy/.test(l) ? "warn" : /ready/.test(l) ? "ok" : "dim";
      return `<span class="${k}">${esc(l)}</span>`;
    };
    const put = (n) => {
      out.innerHTML = lines.slice(0, n).map(fmt).join("\n");
    };
    show("running");
    put(3);

    let busy = false;
    const run = async () => {
      if (busy || lines.length < 8) return;
      busy = true;
      kill.disabled = true;
      put(3);
      show("running");
      await wait(300);
      show("crashed");
      card.classList.remove("shake");
      void card.offsetWidth;
      if (!calm) card.classList.add("shake");
      put(4);
      await wait(380);
      put(5);
      await wait(1000);
      show("starting");
      put(6);
      await wait(900);
      show("running");
      put(7);
      await wait(350);
      put(8);
      kill.disabled = false;
      busy = false;
    };
    kill.addEventListener("click", () => {
      $("#crash").classList.add("touched");
      run();
    });
    if (!calm) whenSeen(card, () => setTimeout(run, 1400), 0.7);
  }

  /* ---------------------------------------------------------------- terminals */

  {
    const desk = $("#desk");
    const esc = (s) => s.replace(/&/g, "&amp;").replace(/</g, "&lt;");
    const color = (l) =>
      esc(l)
        .replace(/^✓/, '<span class="ok">✓</span>')
        .replace(/^● (running|connected)/, '<span class="ok">●</span> $1')
        .replace(/^✕ crashed/, '<span class="bad">✕ crashed</span>')
        .replace(/^◌ starting/, '<span class="warn">◌ starting</span>')
        .replace(/^((STATUS|STATE|\s+ALIAS|ALIAS|ID)\s.*|[-\s]+)$/, '<span class="dim">$1</span>');
    const SCRIPTS = {
      add: [
        ['prot add "Storefront" --cmd "npm run dev" --alias shop --port 5173 --cwd ~/shop', OUT.add1],
        ["prot start shop", OUT.start1],
      ],
      ls: [["prot ls", OUT.ls2]],
      status: [["prot status", OUT.status]],
    };
    const prompt = '<span class="p">$</span> ';

    const render = (el, steps, upto, typed) => {
      let html = "";
      steps.forEach(([cmd, out], i) => {
        if (i > upto) return;
        const c = i === upto ? cmd.slice(0, typed) : cmd;
        html += `${prompt}<span class="c">${esc(c)}</span>\n`;
        if (i < upto || (i === upto && typed >= cmd.length + 1)) html += (out || "").split("\n").map(color).join("\n") + "\n";
      });
      el.innerHTML = html;
    };
    const play = async (el, steps) => {
      for (let i = 0; i < steps.length; i++) {
        const [cmd] = steps[i];
        for (let n = 0; n <= cmd.length; n++) {
          render(el, steps, i, n);
          await wait(calm ? 0 : 20 + Math.random() * 25);
        }
        await wait(calm ? 0 : 260);
        render(el, steps, i, cmd.length + 1);
        await wait(calm ? 0 : 380);
      }
      el.insertAdjacentHTML("beforeend", `${prompt}<span class="caret"></span>`);
    };
    const bodies = $$("[data-type]", desk);
    // every terminal shows its full text from the start (for no-JS-timing and layout); typing replays it
    bodies.forEach((b) => {
      const s = SCRIPTS[b.dataset.type];
      if (s) render(b, s, s.length - 1, 1e9);
    });
    if (!calm) {
      // keep the box sizes while typing: measure the finished text first
      bodies.forEach((b) => (b.style.minHeight = b.offsetHeight + "px"));
      bodies.forEach((b) => (b.textContent = ""));
      // each terminal starts typing when it is itself on screen, not after the ones above it have finished
      bodies.forEach((b) => {
        const s = SCRIPTS[b.dataset.type];
        if (s) whenSeen(b.closest(".win"), () => play(b, s), 0.4);
      });
    }

    // drag a terminal by its title bar and it comes to the front
    let z = 10;
    $$("[data-win]", desk).forEach((w) => {
      const bar = $(".win-bar", w);
      let sx = 0;
      let sy = 0;
      let ox = 0;
      let oy = 0;
      bar.addEventListener("pointerdown", (e) => {
        bar.setPointerCapture(e.pointerId);
        w.style.zIndex = ++z;
        w.classList.add("lift");
        sx = e.clientX;
        sy = e.clientY;
        ox = parseFloat(w.style.getPropertyValue("--dx")) || 0;
        oy = parseFloat(w.style.getPropertyValue("--dy")) || 0;
      });
      bar.addEventListener("pointermove", (e) => {
        if (!w.classList.contains("lift")) return;
        w.style.setProperty("--dx", ox + e.clientX - sx + "px");
        w.style.setProperty("--dy", oy + e.clientY - sy + "px");
      });
      const drop = () => w.classList.remove("lift");
      bar.addEventListener("pointerup", drop);
      bar.addEventListener("pointercancel", drop);
    });
  }

  /* ---------------------------------------------------------------- the cable */

  {
    const sec = $("#tunnel");
    const svg = $("#cable-svg");
    const cardEl = $("#tun-card");
    const rack = $("#rack");
    const plug = $("#plug");
    const paths = ["#cable-shadow", "#cable-edge", "#cable-body", "#cable-shine", "#cable-pulse"].map((s) => $(s));
    const imgs = $$("img", cardEl);
    const show = (s) => imgs.forEach((i) => i.classList.toggle("on", i.dataset.s === s));
    // it starts unplugged: the card says RETRYING until the visitor plugs the cable in
    show("reconnecting");
    sec.classList.add("off");

    let A = { x: 0, y: 0 };
    let S = { x: 0, y: 0 };
    let H = { x: 0, y: 0 };
    let Lmax = 400;
    const pose = { x: 0, y: 0, a: 0, sag: 36 };
    let plugged = false;
    let tween = 0;
    let dragging = false;
    let reconnect = 0;

    // layout offsets, not bounding boxes: an element that is still sliding in (a CSS translate) would otherwise be
    // measured in the wrong place and the plug would sit beside its socket
    const rel = (el) => {
      const l = el.offsetLeft;
      const t = el.offsetTop;
      return { l, t, r: l + el.offsetWidth, b: t + el.offsetHeight, w: el.offsetWidth, h: el.offsetHeight };
    };

    const measure = () => {
      const c = rel(cardEl);
      const k = rel(rack);
      A = { x: c.r - 28, y: c.t + c.h * 0.52 };
      // the slot is cut into the rack from its left edge (14/220 of its width in); the plug tip sits 12px inside it
      S = { x: k.l + (k.w * 14) / 220 + 12, y: k.t + k.h * 0.464 };
      H = { x: clamp(S.x - 130, 120, sec.clientWidth - 120), y: Math.min(sec.clientHeight - 36, S.y + 190) };
      Lmax = Math.hypot(S.x - A.x, S.y - A.y) * 1.16 + 50;
      svg.setAttribute("viewBox", `0 0 ${sec.clientWidth} ${sec.clientHeight}`);
      const ar = $(".arrow-plug", sec);
      ar.style.left = S.x - 129 + "px";
      ar.style.top = S.y + 12 + "px";
    };

    const draw = () => {
      const { x, y, a, sag } = pose;
      const dx = Math.cos(a);
      const dy = Math.sin(a);
      const E = { x: x - dx * 60, y: y - dy * 60 };
      const dist = Math.hypot(E.x - A.x, E.y - A.y);
      const k = Math.min(260, dist * 0.42);
      const c1 = { x: A.x + k, y: A.y + sag * 0.7 };
      const c2 = { x: E.x - dx * k, y: E.y - dy * k + sag * 0.5 };
      const d = `M${A.x.toFixed(1)} ${A.y.toFixed(1)} C${c1.x.toFixed(1)} ${c1.y.toFixed(1)} ${c2.x.toFixed(1)} ${c2.y.toFixed(1)} ${E.x.toFixed(1)} ${E.y.toFixed(1)}`;
      paths.forEach((p, i) => {
        p.setAttribute("d", d);
        if (i === 0) p.setAttribute("transform", "translate(7 9)");
        if (i === 3) p.setAttribute("transform", "translate(-1.5 -2)");
      });
      plug.style.transform = `translate(${(x - 67).toFixed(1)}px, ${(y - 23).toFixed(1)}px) rotate(${a.toFixed(3)}rad)`;
    };

    const ease = {
      out: (t) => 1 - Math.pow(1 - t, 3),
      bounce: (t) => {
        const n = 7.5625;
        const d = 2.75;
        if (t < 1 / d) return n * t * t;
        if (t < 2 / d) return n * (t -= 1.5 / d) * t + 0.75;
        if (t < 2.5 / d) return n * (t -= 2.25 / d) * t + 0.9375;
        return n * (t -= 2.625 / d) * t + 0.984375;
      },
      swing: (t) => 1 - Math.exp(-4.5 * t) * Math.cos(11 * t),
    };

    const go = (to, ms) => {
      cancelAnimationFrame(tween);
      const from = { ...pose };
      if (calm) {
        Object.assign(pose, to);
        draw();
        return;
      }
      const t0 = performance.now();
      const step = (now) => {
        const t = clamp((now - t0) / ms, 0, 1);
        pose.x = lerp(from.x, to.x, ease.out(t));
        pose.y = lerp(from.y, to.y, to.y > from.y ? ease.bounce(t) : ease.out(t));
        pose.a = lerp(from.a, to.a, ease.swing(t));
        pose.sag = lerp(from.sag, to.sag, ease.out(t));
        draw();
        if (t < 1) tween = requestAnimationFrame(step);
      };
      tween = requestAnimationFrame(step);
    };

    const setPlugged = (flag) => {
      plugged = flag;
      plug.setAttribute("aria-pressed", flag);
      plug.setAttribute("aria-label", flag ? "Unplug the cable" : "Plug the cable back in");
      sec.classList.toggle("off", !flag);
      sec.classList.add("touched");
      clearTimeout(reconnect);
      if (flag) {
        go({ x: S.x, y: S.y, a: 0, sag: 36 }, 650);
        // ssh does not come back instantly: the card says so for a moment
        reconnect = setTimeout(() => show("connected"), 1100);
      } else {
        show("reconnecting");
        go({ x: H.x, y: H.y, a: 1.28, sag: 130 }, 1000);
      }
    };

    // drag the plug, or click it
    let moved = 0;
    let grab = { x: 0, y: 0 };
    plug.addEventListener("pointerdown", (e) => {
      plug.setPointerCapture(e.pointerId);
      cancelAnimationFrame(tween);
      dragging = true;
      moved = 0;
      const b = sec.getBoundingClientRect();
      grab = { x: e.clientX - b.left - pose.x, y: e.clientY - b.top - pose.y };
      plug.classList.add("drag");
    });
    plug.addEventListener("pointermove", (e) => {
      if (!dragging) return;
      const b = sec.getBoundingClientRect();
      let x = e.clientX - b.left - grab.x;
      let y = e.clientY - b.top - grab.y;
      moved += Math.abs(e.movementX) + Math.abs(e.movementY);
      const d = Math.hypot(x - A.x, y - A.y);
      if (d > Lmax) {
        x = A.x + ((x - A.x) * Lmax) / d;
        y = A.y + ((y - A.y) * Lmax) / d;
      }
      const away = Math.hypot(x - S.x, y - S.y);
      pose.x = x;
      pose.y = y;
      pose.a = clamp(Math.atan2(y - A.y, x - A.x) + (away > 40 ? 0.45 : 0.08), -0.5, 1.4);
      pose.sag = Math.max(30, (Lmax - Math.hypot(x - A.x, y - A.y)) * 0.55);
      if (moved > 6 && plugged && away > 50) {
        plugged = false;
        plug.setAttribute("aria-pressed", false);
        sec.classList.add("off", "touched");
        show("reconnecting");
      }
      draw();
    });
    const release = () => {
      if (!dragging) return;
      dragging = false;
      plug.classList.remove("drag");
      if (moved <= 6) {
        setPlugged(!plugged);
        return;
      }
      const away = Math.hypot(pose.x - S.x, pose.y - S.y);
      if (away < 60) setPlugged(true);
      else {
        plugged = true; // route through setPlugged so the card and cable agree
        setPlugged(false);
      }
    };
    plug.addEventListener("pointerup", release);
    plug.addEventListener("pointercancel", release);
    plug.addEventListener("keydown", (e) => {
      if (e.key === "Enter" || e.key === " ") {
        e.preventDefault();
        setPlugged(!plugged);
      }
    });

    const settle = () => {
      measure();
      const to = plugged ? { x: S.x, y: S.y, a: 0, sag: 36 } : { x: H.x, y: H.y, a: 1.28, sag: 130 };
      Object.assign(pose, to);
      draw();
    };
    addEventListener("resize", settle);
    addEventListener("load", settle);
    document.fonts?.ready.then(settle);
    settle();
  }

  /* ---------------------------------------------------------------- the status bar */

  {
    const read = $("#hud-read");
    const face = $("#hud-face");
    const img = $("img", face);
    const IDLE = "prot mcp";
    const say = (t, deny) => {
      read.textContent = t;
      read.classList.toggle("deny", Boolean(deny));
    };
    $$("#arms button").forEach((b) => {
      const on = () => say(b.dataset.t);
      b.addEventListener("mouseenter", on);
      b.addEventListener("focus", on);
      b.addEventListener("mouseleave", () => say(IDLE));
      b.addEventListener("blur", () => say(IDLE));
    });
    $$("#locks button").forEach((b) => {
      const on = () => {
        say(b.dataset.t, true);
        face.classList.remove("no");
        void face.offsetWidth;
        if (!calm) face.classList.add("no");
      };
      b.addEventListener("mouseenter", on);
      b.addEventListener("focus", on);
      b.addEventListener("click", on);
      b.addEventListener("mouseleave", () => say(IDLE));
      b.addEventListener("blur", () => say(IDLE));
    });
    if (!calm) {
      // like the Doom face, it looks at whatever you are pointing at
      addEventListener("pointermove", (e) => {
        if (face.classList.contains("no")) return;
        const r = face.getBoundingClientRect();
        if (r.bottom < 0 || r.top > innerHeight) return;
        const dx = clamp((e.clientX - (r.left + r.width / 2)) / 500, -1, 1);
        const dy = clamp((e.clientY - (r.top + r.height / 2)) / 500, -1, 1);
        img.style.transform = `translate(${dx * 7}px, ${dy * 4}px) rotate(${dx * 7}deg)`;
      }, { passive: true });
    }
  }
})();
