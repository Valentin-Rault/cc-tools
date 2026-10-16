//! pr-watch-tabs: a zellij plugin that moves panes to other tabs, for pr-watch.
//!
//! pr-watch sends `zellij pipe --plugin file:…/pr-watch-tabs.wasm --name move -- "<pane id> <placeholder> <tab name>"`,
//! one move per line. To move a pane into a tab that exists, pr-watch first opens a placeholder pane there, and the
//! plugin replaces the placeholder with the pane, which closes the placeholder. With `-` for the placeholder, the pane
//! goes to a new tab of that name, along with the other panes for that tab. The plugin answers "queued" at once, then
//! moves the panes one step at a time, without moving anyone's focus; pr-watch checks where they end up itself.
//!
//! zellij's command line can't move a pane to another tab, and in zellij 0.45.1 its plugin API can't move one into most
//! existing tabs: break_panes_to_tab_with_id and _with_index both check the number they're given against the tabs'
//! positions, then put the pane in the tab with that ID, so they work only while a tab's ID is also a position, which
//! stops once tabs have been closed. Replacing a pane with an existing one works across tabs.

use std::collections::{BTreeMap, VecDeque};
use zellij_tile::prelude::*;

/// How long to wait after a step before the next one, for zellij to settle.
const SETTLE_SECS: f64 = 0.3;

struct Move {
    pane: u32,
    placeholder: Option<u32>, // a pane pr-watch opened in the target tab; None for a new tab
    tab: String,
}

#[derive(Default)]
struct State {
    granted: bool,
    queue: VecDeque<Move>,
    settling: bool, // a timer is set: the next step waits for it
}

register_plugin!(State);

impl ZellijPlugin for State {
    fn load(&mut self, _configuration: BTreeMap<String, String>) {
        request_permission(&[PermissionType::ChangeApplicationState, PermissionType::ReadCliPipes]);
        subscribe(&[EventType::PermissionRequestResult, EventType::Timer]);
    }

    fn update(&mut self, event: Event) -> bool {
        match event {
            Event::PermissionRequestResult(PermissionStatus::Granted) => {
                self.granted = true;
                set_selectable(false);
                hide_self();
            }
            Event::PermissionRequestResult(PermissionStatus::Denied) => self.queue.clear(),
            Event::Timer(_) => self.settling = false,
            _ => {}
        }
        self.step();
        false
    }

    fn pipe(&mut self, message: PipeMessage) -> bool {
        let PipeSource::Cli(pipe) = message.source else {
            return false;
        };
        if message.name != "move" {
            answer(&pipe, "error: expected --name move");
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
        // the moves start on a timer, not here: a move waits for zellij, and zellij drops the answer to a pipe that
        // takes over a second
        if !self.settling {
            self.settling = true;
            set_timeout(0.05);
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
    // step makes the next move, once the previous one has settled. Panes for a new tab all go in one step.
    fn step(&mut self) {
        if !self.granted || self.settling {
            return;
        }
        let Some(m) = self.queue.pop_front() else {
            return;
        };
        match m.placeholder {
            Some(placeholder) => {
                replace_pane_with_existing_pane(PaneId::Terminal(placeholder), PaneId::Terminal(m.pane), false);
            }
            None => {
                let mut panes = vec![PaneId::Terminal(m.pane)];
                self.queue.retain(|o| {
                    let same = o.placeholder.is_none() && o.tab == m.tab;
                    if same {
                        panes.push(PaneId::Terminal(o.pane));
                    }
                    !same
                });
                break_panes_to_new_tab(&panes, Some(m.tab), false);
            }
        }
        self.settling = true;
        set_timeout(SETTLE_SECS);
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
