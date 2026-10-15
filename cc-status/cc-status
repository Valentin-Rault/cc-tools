#!/usr/bin/env python3
"""cc-status: which Claude Code sessions in your zellij panes need you.

  cc-status ui      overview to pick a session and jump to it (meant for a floating zellij pane)
  cc-status list    print the overview once
  cc-status hook    Claude Code hook entry point, reads the event JSON on stdin
  cc-status watch   internal: clears a ✓ once you have visited its pane (the hook starts it)
  cc-status cue     internal: glows and animates the title of the pane the ui just jumped to
  cc-status demo    try the focus cues: demo [glow|title|all] [--jump]  (--jump first moves you to
                    another Claude pane, like picking one in the ui does)

  ● needs you (permission, question, plan approval)   ✓ finished, not visited yet
  ◐ working   · idle   ◌ no hook events from this session yet

State lives in ~/.local/state/cc-status/sessions/<session_id>.json, errors go to ~/.local/state/cc-status/log.
The glow takes its color from CC_STATUS_TINT and its peak strength (0-1) from CC_STATUS_GLOW.
"""
from __future__ import annotations

import fcntl
import json
import os
import random
import re
import select
import shutil
import subprocess
import sys
import termios
import textwrap
import threading
import time
import tty
import unicodedata
from pathlib import Path

STATE_DIR = Path(os.environ.get("CC_STATUS_DIR") or Path.home() / ".local/state/cc-status")
SESSIONS_DIR = STATE_DIR / "sessions"
LOG_FILE = STATE_DIR / "log"
# Claude Code's own live-session files (pid, status busy|idle|shell). Undocumented, so only used as a hint.
CLAUDE_REGISTRY = Path.home() / ".claude/sessions"
ZELLIJ = shutil.which("zellij") or "/opt/homebrew/bin/zellij"

TAB_BADGES = os.environ.get("CC_STATUS_BADGES", "1") != "0"  # append ●✓◐ to zellij tab names
WATCH_INTERVAL = 1.0  # seconds between focus checks while a ✓ is pending
WATCH_DWELL = 2  # consecutive checks a pane must stay focused to count as visited
TERMINAL_APPS = {"Ghostty", "iTerm2", "Terminal", "WezTerm", "kitty", "Alacritty", "Warp"}

TERMINAL_BG = os.environ.get("CC_STATUS_BG", "#282c34")  # Ghostty's default; the glow fades back into it
GLOW_TINT = os.environ.get("CC_STATUS_TINT", "#9d7cd8")
GLOW_STRENGTH = float(os.environ.get("CC_STATUS_GLOW", "0.25"))  # how far the background moves toward the tint
GLOW_SECONDS = 1.4

NEEDS, DONE, WORKING, IDLE, UNTRACKED = "needs", "done", "working", "idle", "untracked"
GLYPH = {NEEDS: "●", DONE: "✓", WORKING: "◐", IDLE: "·", UNTRACKED: "◌"}
COLOR = {NEEDS: "1;31", DONE: "1;32", WORKING: "33", IDLE: "2", UNTRACKED: "2"}
RANK = {NEEDS: 0, DONE: 1, WORKING: 2, IDLE: 3, UNTRACKED: 4}
BADGE_RE = re.compile(r" [●✓◐]+$")
SESSION_ID_RE = re.compile(r"^[A-Za-z0-9_-]{1,128}$")
TITLE_PREFIX_RE = re.compile(r"^[^\w\s]{1,2}\s+")  # the "✳ " Claude puts in front of its topic


# ---------------------------------------------------------------- plumbing


def log(msg: str) -> None:
    try:
        STATE_DIR.mkdir(parents=True, exist_ok=True)
        if LOG_FILE.exists() and LOG_FILE.stat().st_size > 256_000:
            LOG_FILE.write_text("")
        with LOG_FILE.open("a") as f:
            f.write(f"{time.strftime('%Y-%m-%d %H:%M:%S')} {msg}\n")
    except OSError:
        pass


class Lock:
    """Exclusive flock on STATE_DIR/<name>.lock (raises BlockingIOError when non-blocking and taken)."""

    def __init__(self, name: str, blocking: bool = True) -> None:
        self.path = STATE_DIR / f"{name}.lock"
        self.blocking = blocking

    def __enter__(self) -> "Lock":
        STATE_DIR.mkdir(parents=True, exist_ok=True)
        self.file = self.path.open("w")
        try:
            fcntl.flock(self.file, fcntl.LOCK_EX if self.blocking else fcntl.LOCK_EX | fcntl.LOCK_NB)
        except BlockingIOError:
            self.file.close()
            raise
        return self

    def __exit__(self, *exc) -> None:
        self.file.close()


def read_json(path: Path):
    try:
        return json.loads(path.read_text())
    except (OSError, ValueError):
        return None


def run(*cmd: str) -> str:
    try:
        return subprocess.run(cmd, capture_output=True, text=True, timeout=3).stdout
    except (OSError, subprocess.SubprocessError):
        return ""


# ---------------------------------------------------------------- state files


def state_path(session_id: str) -> Path:
    return SESSIONS_DIR / f"{session_id}.json"


def load_states() -> list:
    return [s for s in map(read_json, SESSIONS_DIR.glob("*.json")) if isinstance(s, dict) and "session_id" in s]


def save_state(st: dict) -> None:
    SESSIONS_DIR.mkdir(parents=True, exist_ok=True)
    tmp = SESSIONS_DIR / f".{st['session_id']}.tmp"
    tmp.write_text(json.dumps(st))
    os.replace(tmp, state_path(st["session_id"]))


def drop_state(session_id: str) -> None:
    try:
        state_path(session_id).unlink()
    except FileNotFoundError:
        pass


def mark_seen(session: str, panes: set) -> bool:
    """✓ -> idle for these panes. True if anything changed."""
    changed = False
    with Lock("state"):
        for st in load_states():
            if st.get("zellij") == session and st.get("pane") in panes and st.get("state") == DONE:
                st.update(state=IDLE, kind="idle", since=time.time())
                save_state(st)
                changed = True
    return changed


# ---------------------------------------------------------------- claude processes


def claude_registry() -> dict:
    """sessionId -> Claude Code's registry entry (pid, status, statusUpdatedAt in ms)."""
    entries = {}
    for path in CLAUDE_REGISTRY.glob("*.json"):
        entry = read_json(path)
        if isinstance(entry, dict) and entry.get("sessionId"):
            entries[entry["sessionId"]] = entry
    return entries


def pid_alive(pid) -> bool:
    if not isinstance(pid, int) or pid <= 1:
        return False
    try:
        os.kill(pid, 0)
    except ProcessLookupError:
        return False
    except PermissionError:
        pass
    return True


def find_claude_pid(session_id: str):
    """pid of the claude process running this session: from the registry, else up the process tree."""
    entry = claude_registry().get(session_id)
    if entry and isinstance(entry.get("pid"), int):
        return entry["pid"]
    pid = os.getppid()
    for _ in range(8):
        m = re.match(r"\s*(\d+)\s+(.+)", run("ps", "-o", "ppid=,comm=", "-p", str(pid)))
        if not m:
            return None
        if os.path.basename(m.group(2).strip()) == "claude":
            return pid
        pid = int(m.group(1))
        if pid <= 1:
            return None
    return None


def effective_state(st: dict, reg: dict) -> str:
    """The hook state, corrected when Claude's registry changed after our last event.

    No hook fires for an Esc, a refused prompt, an approved command that is now running,
    or a session waking up by itself, but the registry shows them."""
    state = st.get("state", IDLE)
    entry = reg.get(st.get("session_id"))
    if not entry or entry.get("statusUpdatedAt", 0) / 1000 <= st.get("since", 0) + 1.5:
        return state
    status = entry.get("status")
    if status == "idle" and state in (WORKING, NEEDS):
        return IDLE
    if status == "shell" and state == NEEDS:
        return WORKING
    if status == "busy" and state in (DONE, IDLE):
        return WORKING
    return state


# ---------------------------------------------------------------- zellij


class ZellijError(Exception):
    pass


def zellij(session: str, *args: str) -> str:
    try:
        r = subprocess.run([ZELLIJ, "--session", session, "action", *args], capture_output=True, text=True, timeout=5)
    except (OSError, subprocess.SubprocessError) as e:
        raise ZellijError(str(e)) from e
    if r.returncode != 0:
        raise ZellijError(f"{' '.join(args)}: {r.stderr.strip() or r.returncode}")
    return r.stdout


def list_panes(session: str) -> dict:
    """Terminal pane id -> info (tab_id, tab_position, tab_name, title, pane_command, pane_cwd, ...)."""
    try:
        panes = json.loads(zellij(session, "list-panes", "--all", "--json"))
    except ValueError as e:
        raise ZellijError(f"list-panes: {e}") from e
    return {p["id"]: p for p in panes if not p.get("is_plugin")}


def list_tabs(session: str) -> list:
    try:
        return json.loads(zellij(session, "list-tabs", "--json"))
    except ValueError as e:
        raise ZellijError(f"list-tabs: {e}") from e


def focused_panes(session: str) -> set:
    """Terminal panes that an attached client has focused."""
    focused = set()
    for line in zellij(session, "list-clients").splitlines()[1:]:
        parts = line.split()
        if len(parts) > 1 and parts[1].startswith("terminal_") and parts[1][9:].isdigit():
            focused.add(int(parts[1][9:]))
    return focused


def terminal_frontmost() -> bool:
    """Focus inside zellij only counts as a visit while the terminal app is in front."""
    if not shutil.which("lsappinfo"):
        return True
    info = run("lsappinfo", "info", "-only", "name", run("lsappinfo", "front").strip())
    m = re.search(r'"LSDisplayName"="([^"]+)"', info) or re.search(r'"([^"]+)"', info)
    return bool(m) and m.group(1) in TERMINAL_APPS


def pane_in_view(session: str, pane: int) -> bool:
    try:
        return pane in focused_panes(session) and terminal_frontmost()
    except ZellijError:
        return False


def runs_claude(pane) -> bool:
    command = (pane or {}).get("pane_command") or ""
    return os.path.basename(command.split(" ")[0]) == "claude"


def live_states(session: str, panes: dict) -> list:
    """This zellij session's states whose claude process and pane still exist, newest per pane.

    Everything else is deleted."""
    newest = {}
    for st in load_states():
        if st.get("zellij") != session:
            continue
        pane = panes.get(st.get("pane"))
        alive = pid_alive(st["pid"]) if st.get("pid") else runs_claude(pane)
        older = newest.get(st.get("pane"))
        if not pane or not alive or (older and older.get("since", 0) >= st.get("since", 0)):
            drop_state(st["session_id"])
            continue
        if older:
            drop_state(older["session_id"])
        newest[st["pane"]] = st
    return list(newest.values())


def refresh_badges(session: str) -> None:
    """End each tab name with one glyph per session in it that needs you, is unvisited, or works."""
    if not TAB_BADGES:
        return
    try:
        with Lock("badges"):
            panes, tabs, reg = list_panes(session), list_tabs(session), claude_registry()
            marks = {}
            for st in live_states(session, panes):
                state = effective_state(st, reg)
                if state in (NEEDS, DONE, WORKING):
                    marks.setdefault(panes[st["pane"]].get("tab_id"), []).append(state)
            for tab in tabs:
                base = BADGE_RE.sub("", tab.get("name", ""))
                glyphs = "".join(GLYPH[s] for s in sorted(marks.get(tab.get("tab_id"), []), key=RANK.get))
                name = f"{base} {glyphs}" if glyphs else base
                if name != tab.get("name"):
                    zellij(session, "rename-tab-by-id", str(tab["tab_id"]), name)
    except ZellijError as e:
        log(f"badges: {e}")


# ---------------------------------------------------------------- hook


def one_line(text, limit: int = 300) -> str:
    text = " ".join(str(text or "").split())
    return text if len(text) <= limit else text[: limit - 1] + "…"


def describe_tool(tool: str, tool_input) -> str:
    ti = tool_input if isinstance(tool_input, dict) else {}
    arg = next((ti[k] for k in ("command", "file_path", "url", "pattern", "prompt") if ti.get(k)), "")
    return one_line(f"{tool}: {arg}" if arg else tool)


def classify(event: str, p: dict):
    """Hook event -> (state, kind, detail), or None when it doesn't change the state."""
    tool, tool_input = p.get("tool_name") or "", p.get("tool_input")
    if event in ("PreToolUse", "PermissionRequest"):
        if tool == "AskUserQuestion":
            questions = tool_input.get("questions") if isinstance(tool_input, dict) else None
            first = questions[0] if isinstance(questions, list) and questions else {}
            return NEEDS, "question", one_line(first.get("question") if isinstance(first, dict) else "")
        if tool == "ExitPlanMode":
            return NEEDS, "plan", "plan ready for approval"
        if event == "PermissionRequest":
            return NEEDS, "permission", describe_tool(tool, tool_input)
        return None
    if event == "Notification":
        kind, message = p.get("notification_type") or "", p.get("message") or ""
        if kind == "permission_prompt" or (not kind and "permission" in message.lower()):
            return NEEDS, "permission", one_line(message)
        if kind == "elicitation_dialog":
            return NEEDS, "input", one_line(message)
        return None
    return {
        "SessionStart": (IDLE, "idle", ""),
        "UserPromptSubmit": (WORKING, "working", ""),
        "PostToolUse": (WORKING, "working", ""),
        "PostToolUseFailure": (WORKING, "working", ""),
        "PermissionDenied": (WORKING, "working", ""),
        "Stop": (DONE, "done", one_line(p.get("last_assistant_message"))),
        "StopFailure": (DONE, "error", one_line(p.get("error") or p.get("reason") or "stopped on an error")),
    }.get(event)


def cmd_hook() -> None:
    session, pane = os.environ.get("ZELLIJ_SESSION_NAME"), os.environ.get("ZELLIJ_PANE_ID", "")
    if not session or not pane.isdigit():
        return  # claude isn't running inside zellij
    p = json.loads(sys.stdin.read() or "{}")
    session_id, event, pane_id = p.get("session_id") or "", p.get("hook_event_name") or "", int(pane)
    if not SESSION_ID_RE.match(session_id):
        return
    if event == "SessionEnd":
        with Lock("state"):
            drop_state(session_id)
        refresh_badges(session)
        return
    change = classify(event, p)
    if not change:
        return
    state, kind, detail = change
    current = read_json(state_path(session_id)) or {}
    same_pane = current.get("zellij") == session and current.get("pane") == pane_id
    if same_pane and current.get("state") == state and event != "SessionStart":
        return  # most tool events end here
    if state == DONE and pane_in_view(session, pane_id):
        state, kind = IDLE, "idle"  # you are looking at it
    with Lock("state"):
        current = read_json(state_path(session_id)) or {}
        keep_pid = same_pane and event != "SessionStart" and pid_alive(current.get("pid"))
        save_state({
            "session_id": session_id, "state": state, "kind": kind, "detail": detail,
            "since": time.time(), "zellij": session, "pane": pane_id,
            "cwd": p.get("cwd") or current.get("cwd") or "",
            "pid": current["pid"] if keep_pid else find_claude_pid(session_id),
        })
    refresh_badges(session)
    if state == DONE:
        spawn("watch")


# ---------------------------------------------------------------- watcher


def spawn(*args: str) -> None:
    """Run `cc-status <args>` in the background, detached so it outlives this pane."""
    subprocess.Popen([sys.executable, os.path.realpath(__file__), *args], stdin=subprocess.DEVNULL,
                     stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, start_new_session=True)


def cmd_watch() -> None:
    """Runs while some session is ✓ and marks it seen once its pane has been focused for a moment."""
    try:
        _lock = Lock("watch", blocking=False).__enter__()  # held until the process exits
    except BlockingIOError:
        return
    streaks, rounds = {}, 0
    while True:
        pending = {}
        for st in load_states():
            if st.get("state") == DONE:
                pending.setdefault(st.get("zellij"), set()).add(st.get("pane"))
        if not pending:
            return
        front = terminal_frontmost()
        try:
            rounds += 1
            if rounds % 60 == 0:  # forget sessions whose claude or zellij went away
                for session in pending:
                    try:
                        live_states(session, list_panes(session))
                    except ZellijError:
                        for st in load_states():
                            if st.get("zellij") == session and not pid_alive(st.get("pid")):
                                drop_state(st["session_id"])
            focused = set()
            if front:
                for session in pending:
                    try:
                        focused |= {(session, pane) for pane in focused_panes(session)}
                    except ZellijError:
                        pass
            streaks = {key: streaks.get(key, 0) + 1 for key in focused}
            for session, panes in pending.items():
                visited = {pane for pane in panes if streaks.get((session, pane), 0) >= WATCH_DWELL}
                if visited and mark_seen(session, visited):
                    refresh_badges(session)
        except Exception as e:
            log(f"watch: {type(e).__name__}: {e}")
        time.sleep(WATCH_INTERVAL if front else WATCH_INTERVAL * 3)


# ---------------------------------------------------------------- focus cues


def quietly(session: str, *args: str) -> None:
    """A zellij action whose failure doesn't matter, like undoing a change on a pane that has since closed."""
    try:
        zellij(session, *args)
    except ZellijError:
        pass


def timeline(seconds: float, fps: int = 40):
    """Progress from 0 toward 1 over `seconds` of wall time, one frame at a time."""
    start = time.monotonic()
    while (t := (time.monotonic() - start) / seconds) < 1:
        yield t
        time.sleep(1 / fps)


def smoothstep(t: float) -> float:
    t = min(1.0, max(0.0, t))
    return t * t * (3 - 2 * t)


def hex_rgb(color: str) -> tuple:
    color = color.lstrip("#")
    return tuple(int(color[i : i + 2], 16) for i in (0, 2, 4))


def cue_glow(session: str, pane: dict) -> None:
    """The pane's background breathes toward GLOW_TINT and back, with no hard edge on either side."""
    target, base, tint = f"terminal_{pane['id']}", hex_rgb(TERMINAL_BG), hex_rgb(GLOW_TINT)
    shown = ""
    try:
        for t in timeline(GLOW_SECONDS):
            level = GLOW_STRENGTH * (smoothstep(t / 0.2) if t < 0.2 else 1 - smoothstep((t - 0.2) / 0.8))
            color = "#" + "".join(f"{round(b + (c - b) * level):02x}" for b, c in zip(base, tint))
            if color != shown:
                zellij(session, "set-pane-color", "-p", target, "--bg", color)
                shown = color
    finally:
        quietly(session, "set-pane-color", "-p", target, "--reset")


# Same width on every frame, so the title itself stays put while the chevrons move.
TITLE_SWEEP = [("❯  ", "  ❮"), ("❯❯ ", " ❮❮"), ("❯❯❯", "❮❮❮"), (" ❯❯", "❮❮ "), ("  ❯", "❮  ")]
TITLE_TWINKLE = [("  ✦", "✦  "), ("  ✧", "✧  "), ("  ✦", "✦  ")]


def cue_title(session: str, pane: dict) -> None:
    """Chevrons sweep in toward the pane's frame title three times, then it twinkles."""
    target = f"terminal_{pane['id']}"
    title = TITLE_PREFIX_RE.sub("", pane.get("title") or "").strip() or "claude"
    try:
        for (left, right), pause in [(f, 0.08) for f in TITLE_SWEEP * 3] + [(f, 0.15) for f in TITLE_TWINKLE]:
            zellij(session, "rename-pane", "-p", target, f"{left} {title} {right}")
            time.sleep(pause)
    finally:
        quietly(session, "undo-rename-pane", "-p", target)


CUES = {"glow": cue_glow, "title": cue_title}


def play_cues(session: str, pane_id: int, names: list) -> None:
    """Play these cues on the pane at once. Each one undoes its own changes when it ends, even on an error."""
    pane = list_panes(session).get(pane_id)
    if not pane:
        return

    def play(name: str) -> None:
        try:
            CUES[name](session, pane)
        except ZellijError as e:
            log(f"cue {name}: {e}")
            print(f"cc-status: cue {name}: {e}", file=sys.stderr)

    threads = [threading.Thread(target=play, args=(name,)) for name in names]
    for thread in threads:
        thread.start()
    for thread in threads:
        thread.join()


# ---------------------------------------------------------------- overview


def ago(ts: float) -> str:
    if not ts:
        return ""
    s = max(0, int(time.time() - ts))
    for unit, size in (("d", 86400), ("h", 3600), ("m", 60)):
        if s >= size:
            return f"{s // size}{unit}"
    return f"{s}s"


def cells(ch: str) -> int:
    if unicodedata.combining(ch):
        return 0
    return 2 if unicodedata.east_asian_width(ch) in ("W", "F") else 1


def fit(text: str, width: int) -> str:
    """Pad or cut (with …) to exactly `width` terminal cells."""
    if width <= 0:
        return ""
    used = sum(map(cells, text))
    if used <= width:
        return text + " " * (width - used)
    out, used = [], 0
    for ch in text:
        if used + cells(ch) > width - 1:
            break
        out.append(ch)
        used += cells(ch)
    return "".join(out) + "…" + " " * (width - 1 - used)


def make_row(session: str, pane: dict, state: str, kind: str, detail: str, since: float, cwd, bg: bool) -> dict:
    title = TITLE_PREFIX_RE.sub("", pane.get("title") or "").strip()
    if not title or re.fullmatch(r"Pane #\d+", title):
        title = "claude"
    return {
        "zellij": session, "pane": pane["id"], "tab_id": pane.get("tab_id"),
        "tab_position": pane.get("tab_position", 0), "tab": BADGE_RE.sub("", pane.get("tab_name") or ""),
        "title": title, "state": state, "kind": kind, "detail": detail, "since": since,
        "project": os.path.basename((cwd or "").rstrip("/")) or "~", "bg": bg,
    }


def collect() -> list:
    """One row per Claude pane: hook state joined with zellij's panes and Claude's registry."""
    here = os.environ.get("ZELLIJ_SESSION_NAME")
    states, reg = load_states(), claude_registry()
    sessions = {st.get("zellij") for st in states if st.get("zellij")} | ({here} if here else set())
    rows = []
    for session in sorted(sessions):
        try:
            panes = list_panes(session)
        except ZellijError:
            continue
        tracked = set()
        for st in live_states(session, panes):
            tracked.add(st["pane"])
            state = effective_state(st, reg)
            kind = (st.get("kind") or state) if state == st.get("state") else state
            bg = state in (DONE, IDLE) and reg.get(st["session_id"], {}).get("status") == "shell"
            rows.append(make_row(session, panes[st["pane"]], state, kind, st.get("detail") or "",
                                 st.get("since", 0), st.get("cwd"), bg))
        for pane_id, pane in panes.items():
            if pane_id not in tracked and runs_claude(pane):
                rows.append(make_row(session, pane, UNTRACKED, "untracked", "no hook events from this session yet",
                                     0, pane.get("pane_cwd"), False))
    rows.sort(key=lambda r: (RANK[r["state"]], r["since"] if r["state"] in (NEEDS, DONE) else 0,
                             r["tab_position"], r["pane"]))
    return rows


def layout(rows: list, selected: int, width: int, height: int, color: bool, interactive: bool) -> list:
    def paint(code: str, text: str) -> str:
        return f"\x1b[{code}m{text}\x1b[0m" if color else text

    width = max(width, 40)
    counts = [f"{n} {label}" for n, label in (
        (sum(r["state"] == NEEDS for r in rows), "need you"),
        (sum(r["state"] == DONE for r in rows), "to review"),
        (sum(r["state"] == WORKING for r in rows), "working")) if n]
    summary = " · ".join(counts) or "nothing needs you"
    heading = " Claude sessions"
    lines = [paint("1", heading) + " " * max(1, width - len(heading) - len(summary) - 1) + summary, ""]
    if not rows:
        lines.append("  No Claude panes in this zellij session.")
        return lines + (["", paint("2", "  q quit")] if interactive else [])

    multi = len({r["zellij"] for r in rows}) > 1
    tabs = [f"{r['zellij']}:{r['tab']}" if multi else r["tab"] for r in rows]
    tab_w = min(max(map(len, tabs)), 24)
    proj_w = min(max(len(r["project"]) for r in rows), 24)
    title_w = max(10, min(60, width - 32 - tab_w - proj_w))
    visible = max(1, height - 9) if interactive else len(rows)
    top = min(max(0, selected - visible + 1), max(0, len(rows) - visible))
    for i in range(top, min(len(rows), top + visible)):
        r = rows[i]
        num = str(i + 1) if i < 9 else " "
        age = ago(r["since"]) if r["state"] != UNTRACKED else ""
        mark = f"{GLYPH[r['state']]} {fit(r['kind'], 10)}"
        rest = (f"  {fit(tabs[i], tab_w)}  {fit(r['title'], title_w)}  {fit(r['project'], proj_w)}"
                f"  {age:>4}{' bg' if r['bg'] else '   '}")
        if i == selected:
            lines.append(paint("7", f" {num} {mark}{rest}"))
        else:
            lines.append(f" {paint('2', num)} {paint(COLOR[r['state']], mark)}{rest}")
        if not interactive and r["state"] in (NEEDS, DONE) and r["detail"]:
            lines.append(paint("2", f"       └ {fit(r['detail'], width - 10).rstrip()}"))
    if interactive and 0 <= selected < len(rows):
        r = rows[selected]
        lines.append("")
        lines.append(paint("2", "  " + fit(f"{r['zellij']} › {r['tab']} › terminal_{r['pane']} · {r['project']}",
                                           width - 2).rstrip()))
        lines.extend("  " + chunk for chunk in (textwrap.wrap(r["detail"], width - 4)[:3] or [""]))
        lines.append("")
        lines.append(paint("2", "  ↑↓ move   ⏎ or 1-9 jump   r refresh   q quit"))
    return lines


def jump(r: dict) -> None:
    here = os.environ.get("ZELLIJ_SESSION_NAME", "")
    target = f"terminal_{r['pane']}"
    try:
        if r["zellij"] == here:
            zellij(here, "go-to-tab-by-id", str(r["tab_id"]))
            try:
                zellij(here, "focus-pane-id", target)
            except ZellijError as e:
                if "already focused" not in str(e):  # like the pane right behind this popup
                    raise
        else:
            zellij(here, "switch-session", r["zellij"], "--tab-position", str(r["tab_position"]), "--pane-id", target)
    except ZellijError as e:
        log(f"jump: {e}")
        return
    spawn("cue", r["zellij"], str(r["pane"]))
    if r["state"] == DONE and mark_seen(r["zellij"], {r["pane"]}):
        refresh_badges(r["zellij"])


def cmd_ui() -> None:
    here = os.environ.get("ZELLIJ_SESSION_NAME")
    if not here or not sys.stdin.isatty():
        sys.exit("cc-status ui: run it inside a zellij pane")
    fd, out = sys.stdin.fileno(), sys.stdout
    saved = termios.tcgetattr(fd)
    selected, rows, fetched, first = 0, [], 0.0, True
    try:
        tty.setcbreak(fd)
        out.write("\x1b[?1049h\x1b[?25l")
        while True:
            if time.time() - fetched >= 1.0:
                rows, fetched = collect(), time.time()
            selected = max(0, min(selected, len(rows) - 1))
            size = shutil.get_terminal_size()
            frame = layout(rows, selected, size.columns, size.lines, color=True, interactive=True)
            out.write("\x1b[H" + "\x1b[K\n".join(frame[: size.lines]) + "\x1b[K\x1b[J")
            out.flush()
            if first:  # after the first frame so the popup opens fast
                first = False
                refresh_badges(here)
                if any(st.get("state") == DONE for st in load_states()):
                    spawn("watch")
            if not select.select([fd], [], [], 1.0)[0]:
                continue
            key = os.read(fd, 32)
            if key in (b"q", b"\x1b", b"\x03"):
                return
            if key in (b"\x1b[A", b"\x1bOA", b"k"):
                selected -= 1
            elif key in (b"\x1b[B", b"\x1bOB", b"j"):
                selected += 1
            elif key in (b"\r", b"\n") and rows:
                jump(rows[selected])
                return
            elif len(key) == 1 and b"1" <= key <= b"9" and int(key) <= len(rows):
                jump(rows[int(key) - 1])
                return
            elif key == b"r":
                fetched = 0.0
    finally:
        out.write("\x1b[?25h\x1b[?1049l")
        out.flush()
        termios.tcsetattr(fd, termios.TCSADRAIN, saved)


def cmd_list() -> None:
    rows = collect()
    width = shutil.get_terminal_size((120, 40)).columns
    for line in layout(rows, -1, width, 0, color=sys.stdout.isatty(), interactive=False):
        print(line)


def cmd_cue() -> None:
    session, pane = sys.argv[2], int(sys.argv[3])
    try:
        play_cues(session, pane, list(CUES))
    except ZellijError as e:
        log(f"cue: {e}")


def cmd_demo() -> None:
    session = os.environ.get("ZELLIJ_SESSION_NAME")
    if not session:
        sys.exit("cc-status demo: run it inside zellij")
    args = sys.argv[2:]
    picked = next((a for a in args if not a.startswith("-")), "all")
    names = list(CUES) if picked == "all" else picked.split(",")
    if any(name not in CUES for name in names):
        sys.exit(f"cc-status demo: pick from {', '.join(CUES)}, or all")
    try:
        panes = list_panes(session)
        here = next(iter(focused_panes(session) & panes.keys()), None)
        target = panes.get(here)
        if "--jump" in args:
            others = [p for p in panes.values() if p["id"] != here and not p.get("is_floating")]
            if not others:
                sys.exit("cc-status demo: no other pane to jump to")
            target = random.choice([p for p in others if runs_claude(p)] or others)
            zellij(session, "go-to-tab-by-id", str(target["tab_id"]))
            zellij(session, "focus-pane-id", f"terminal_{target['id']}")
        if not target:
            sys.exit("cc-status demo: found no focused pane")
        print(f"{' + '.join(names)} on terminal_{target['id']} in {BADGE_RE.sub('', target.get('tab_name') or '')}")
        play_cues(session, target["id"], names)
    except ZellijError as e:
        sys.exit(f"cc-status demo: {e}")


def main() -> None:
    command = sys.argv[1] if len(sys.argv) > 1 else ""
    if command == "hook":
        # A hook must stay silent and succeed: some events feed stdout to Claude, and exit 2 blocks tools.
        sys.stdout = sys.stderr = open(os.devnull, "w")
        try:
            cmd_hook()
        except Exception as e:
            log(f"hook: {type(e).__name__}: {e}")
        return
    commands = {"ui": cmd_ui, "list": cmd_list, "watch": cmd_watch, "cue": cmd_cue, "demo": cmd_demo}
    if command not in commands:
        sys.stderr.write(__doc__)
        sys.exit(1)
    try:
        commands[command]()
    except KeyboardInterrupt:
        pass


if __name__ == "__main__":
    main()
