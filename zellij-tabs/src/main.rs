//! pr-watch-tabs: a zellij plugin that moves panes to other tabs, for pr-watch.
//!
//! pr-watch sends `zellij pipe --plugin file:…/pr-watch-tabs.wasm --plugin-configuration protocol=2 --name place --
//! "<pane id> <placeholder> <tab name>"`, one move per line, and the plugin answers "queued" at once. To move a pane
//! into a tab that exists, pr-watch first opens a floating placeholder pane there; the plugin swaps the pane in for it
//! and then embeds it, so zellij places it among the tab's tiled panes the way it places a new pane. With `-` for the
//! placeholder, the pane goes to a new tab of that name, along with the other panes for that tab. Nobody's focus
//! moves; pr-watch checks where the panes end up itself.
//!
//! Why so roundabout, in zellij 0.45.1: its command line can't move a pane to another tab, and its plugin API
//! can't move one into most existing tabs. break_panes_to_tab_with_id and _with_index check the number they're given
//! against the tabs' positions, then look the tab up by ID: they work only while a tab's ID is also its position,
//! which stops once tabs have been closed. replace_pane_with_existing_pane works across tabs, but it takes the pane out
//! of its own tab first and drops it when the pane it replaces isn't tiled or floating: a pane collapsed in a stack is
//! neither, as zellij keeps all but the shown member of a stack among its suppressed panes once the tab is drawn.
//!
//! So the placeholder floats: a floating pane always fits, is never in a stack, and opening one leaves the tab's
//! tiled panes as they are. Swapped in for it, the pane floats in its new tab; embedding it has zellij add it to the
//! tiled panes as it adds any new pane: by the tab's swap layout while the tab follows one (into its stack, with a
//! layout that has one, and the tab keeps following it), otherwise by splitting a pane with room to spare, or into a
//! stack. A placeholder that's a split instead would shrink a pane on every move, and stop the tab following its swap
//! layout, as any split does.
//!
//! The plugin acts on what zellij reports of its panes (PaneUpdate), never on a guess: it swaps a pane in only for a
//! placeholder zellij lists as floating, and it embeds the pane only once zellij lists it floating in the
//! placeholder's tab, since embedding is a toggle that would float a tiled pane. A step whose pane doesn't show up as
//! expected within a few seconds is left alone, and pr-watch asks again.

use std::collections::{BTreeMap, VecDeque};
use zellij_tile::prelude::*;

/// How long to wait after a step before the next one, for zellij to settle.
const SETTLE_SECS: f64 = 0.3;
/// How often a step waiting for zellij to report a pane looks again, and how many times.
const POLL_SECS: f64 = 0.1;
const POLLS: u32 = 30;

struct Move {
    pane: u32,
    placeholder: Option<u32>, // a floating pane pr-watch opened in the target tab; None for a new tab
    tab: String,
}

/// What the move in progress waits for.
enum Step {
    Idle,
    /// zellij to list the placeholder, floating, before the pane is swapped in.
    Swap { m: Move, polls: u32 },
    /// zellij to list the pane floating in the placeholder's tab (by position) before it's embedded.
    Embed { pane: u32, tab: usize, polls: u32 },
}

struct State {
    granted: bool,
    panes: Option<PaneManifest>,
    queue: VecDeque<Move>,
    step: Step,
    waiting: bool, // a timer is set: the next step waits for it
}

impl Default for State {
    fn default() -> Self {
        State { granted: false, panes: None, queue: VecDeque::new(), step: Step::Idle, waiting: false }
    }
}

register_plugin!(State);

impl ZellijPlugin for State {
    fn load(&mut self, _configuration: BTreeMap<String, String>) {
        request_permission(&[
            PermissionType::ChangeApplicationState,
            PermissionType::ReadCliPipes,
            PermissionType::ReadApplicationState,
        ]);
        subscribe(&[EventType::PermissionRequestResult, EventType::Timer, EventType::PaneUpdate]);
    }

    fn update(&mut self, event: Event) -> bool {
        match event {
            Event::PermissionRequestResult(PermissionStatus::Granted) => {
                self.granted = true;
                set_selectable(false);
                hide_self();
            }
            Event::PermissionRequestResult(PermissionStatus::Denied) => self.queue.clear(),
            Event::PaneUpdate(panes) => self.panes = Some(panes),
            Event::Timer(_) => self.waiting = false,
            _ => {}
        }
        self.advance();
        false
    }

    fn pipe(&mut self, message: PipeMessage) -> bool {
        let PipeSource::Cli(pipe) = message.source else {
            return false;
        };
        if message.name != "place" {
            answer(&pipe, "error: expected --name place");
            return false;
        }
        let mut moves = vec![];
        for line in message.payload.unwrap_or_default().lines().filter(|l| !l.trim().is_empty()) {
            match parse(line) {
                Some(m) => moves.push(m),
                None => {
                    answer(&pipe, &format!("error: expected \"<pane id> <placeholder pane id or -> <tab name>\", got {line:?}"));
                    return false;
                }
            }
        }
        let waiting = if self.granted { "" } else { " (waiting for its permissions in zellij)" };
        answer(&pipe, &format!("queued {}{waiting}", moves.len()));
        self.queue.extend(moves);
        // the moves start on a timer, not here: a swap waits for zellij, and zellij drops the answer to a pipe that
        // takes over a second
        if !self.waiting {
            self.wait(0.05);
        }
        false
    }

    fn render(&mut self, _rows: usize, _cols: usize) {
        if !self.granted {
            println!("pr-watch-tabs moves Claude sessions to tabs by stage for pr-watch.");
            println!("Grant it the permissions zellij asks for; it then hides itself.");
        }
    }
}

impl State {
    /// advance takes the moves one step at a time, each once the previous one has settled.
    fn advance(&mut self) {
        if !self.granted || self.waiting {
            return;
        }
        loop {
            match std::mem::replace(&mut self.step, Step::Idle) {
                Step::Idle => {
                    let Some(m) = self.queue.pop_front() else {
                        return;
                    };
                    if m.placeholder.is_some() {
                        self.step = Step::Swap { m, polls: 0 };
                        continue;
                    }
                    // panes for a new tab all go in one step
                    let mut panes = vec![PaneId::Terminal(m.pane)];
                    self.queue.retain(|o| {
                        let same = o.placeholder.is_none() && o.tab == m.tab;
                        if same {
                            panes.push(PaneId::Terminal(o.pane));
                        }
                        !same
                    });
                    break_panes_to_new_tab(&panes, Some(m.tab), false);
                    return self.wait(SETTLE_SECS);
                }
                Step::Swap { m, polls } => {
                    let placeholder = m.placeholder.unwrap_or_default();
                    match self.find(placeholder) {
                        Some((tab, p)) if p.is_floating && !p.is_suppressed => {
                            replace_pane_with_existing_pane(PaneId::Terminal(placeholder), PaneId::Terminal(m.pane), false);
                            self.step = Step::Embed { pane: m.pane, tab, polls: 0 };
                            return self.wait(POLL_SECS);
                        }
                        Some(_) => {} // not floating: swapping into it could lose the pane
                        None if polls < POLLS => {
                            self.step = Step::Swap { m, polls: polls + 1 };
                            return self.wait(POLL_SECS);
                        }
                        None => {} // it never showed up
                    }
                }
                Step::Embed { pane, tab, polls } => match self.find(pane) {
                    Some((t, p)) if t == tab && p.is_floating => {
                        toggle_pane_embed_or_eject_for_pane_id(PaneId::Terminal(pane));
                        return self.wait(SETTLE_SECS);
                    }
                    _ if polls < POLLS => {
                        self.step = Step::Embed { pane, tab, polls: polls + 1 };
                        return self.wait(POLL_SECS);
                    }
                    _ => {} // it never arrived floating: it stays where it is
                },
            }
        }
    }

    fn wait(&mut self, secs: f64) {
        self.waiting = true;
        set_timeout(secs);
    }

    /// find is the tab position and pane of a terminal pane, as zellij last reported them.
    fn find(&self, id: u32) -> Option<(usize, &PaneInfo)> {
        self.panes.as_ref()?.panes.iter().find_map(|(tab, panes)| {
            panes.iter().find(|p| !p.is_plugin && p.id == id).map(|p| (*tab, p))
        })
    }
}

// parse reads "<pane id> <placeholder pane id or -> <tab name>"; pane IDs may have zellij's "terminal_" prefix.
fn parse(line: &str) -> Option<Move> {
    let pane_id = |s: &str| s.trim_start_matches("terminal_").parse::<u32>().ok();
    let (pane, rest) = line.trim().split_once(' ')?;
    let (placeholder, tab) = rest.trim_start().split_once(' ')?;
    let tab = tab.trim();
    Some(Move {
        pane: pane_id(pane)?,
        placeholder: if placeholder == "-" { None } else { Some(pane_id(placeholder)?) },
        tab: (!tab.is_empty()).then(|| tab.to_string())?,
    })
}

fn answer(pipe: &str, text: &str) {
    cli_pipe_output(pipe, text);
    unblock_cli_pipe_input(pipe);
}
