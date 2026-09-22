#![cfg_attr(not(debug_assertions), windows_subsystem = "windows")]

fn main() {
    ssh_tunnel_lib::run()
}