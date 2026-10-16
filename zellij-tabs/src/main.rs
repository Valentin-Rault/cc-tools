//! pr-watch-tabs: a zellij plugin that moves panes to tabs by name, for pr-watch.
//!
//! pr-watch sends `zellij pipe --plugin file:…/pr-watch-tabs.wasm --name move -- "<pane id> <tab name>"`. The
//! plugin moves that terminal pane to the tab with that name, or to a new tab of that name when there's none,
//! without moving anyone's focus. zellij's command line can't move a pane to another tab; its plugin API can.

use std::collections::{BTreeMap, HashSet};
use zellij_tile::prelude::*;

#[derive(Default)]
struct State {
    tabs: Vec<TabInfo>,
    granted: bool,
    waiting: Vec<Move>,          // arrived before the permissions, or while their tab is being created
    creating: HashSet<String>,   // tabs asked for, not in a TabUpdate yet
}

struct Move {
    pipe: String, // the CLI pipe to answer
    pane: u32,
    tab: String,
}

register_plugin!(State);

impl ZellijPlugin for State {
    fn load(&mut self, _configuration: BTreeMap<String, String>) {
        request_permission(&[
            PermissionType::ReadApplicationState,
            PermissionType::ChangeApplicationState,
            PermissionType::ReadCliPipes,
        ]);
        subscribe(&[EventType::TabUpdate, EventType::PermissionRequestResult]);
    }

    fn update(&mut self, event: Event) -> bool {
        match event {
            Event::PermissionRequestResult(PermissionStatus::Granted) => {
                self.granted = true;
                set_selectable(false);
                hide_self();
                self.run();
            }
            Event::PermissionRequestResult(PermissionStatus::Denied) => {
                for m in std::mem::take(&mut self.waiting) {
                    answer(&m.pipe, "error: pr-watch-tabs was denied the permissions it needs");
                }
            }
            Event::TabUpdate(tabs) => {
                self.creating.retain(|name| !tabs.iter().any(|t| base(&t.name) == name));
                self.tabs = tabs;
                self.run();
            }
            _ => {}
        }
        false
    }

    fn pipe(&mut self, message: PipeMessage) -> bool {
        let PipeSource::Cli(pipe) = message.source else {
            return false;
        };
        let payload = message.payload.unwrap_or_default();
        let parsed = payload.trim().split_once(' ').and_then(|(pane, tab)| {
            let pane = pane.trim_start_matches("terminal_").parse::<u32>().ok()?;
            Some((pane, tab.trim().to_string()))
        });
        match (message.name.as_str(), parsed) {
            ("move", Some((pane, tab))) if !tab.is_empty() => {
                self.waiting.push(Move { pipe, pane, tab });
                self.run();
            }
            _ => answer(&pipe, "error: expected --name move -- \"<pane id> <tab name>\""),
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
    // run does the moves it can: a move to a tab being created waits for the TabUpdate that shows it.
    fn run(&mut self) {
        if !self.granted || self.tabs.is_empty() {
            return;
        }
        for m in std::mem::take(&mut self.waiting) {
            if self.creating.contains(&m.tab) {
                self.waiting.push(m);
                continue;
            }
            let pane = [PaneId::Terminal(m.pane)];
            match self.tabs.iter().find(|t| base(&t.name) == m.tab) {
                Some(tab) => {
                    break_panes_to_tab_with_id(&pane, tab.tab_id, false);
                }
                None => {
                    break_panes_to_new_tab(&pane, Some(m.tab.clone()), false);
                    self.creating.insert(m.tab.clone());
                }
            }
            answer(&m.pipe, "moved");
        }
    }
}

// base is a tab's name without the badges cc-status appends to it (" ●", " ✓", " ◐").
fn base(name: &str) -> &str {
    name.trim_end_matches(['●', '✓', '◐']).trim_end()
}

fn answer(pipe: &str, text: &str) {
    cli_pipe_output(pipe, text);
    unblock_cli_pipe_input(pipe);
}
