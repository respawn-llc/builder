// In release builds, use the Windows GUI subsystem so launching the desktop app
// never allocates/flashes a console window. Debug builds keep the console for
// log visibility.
#![cfg_attr(not(debug_assertions), windows_subsystem = "windows")]

fn main() {
    app_desktop_lib::run()
}
