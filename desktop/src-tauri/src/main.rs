//! Go Eat desktop shell.
//!
//! A thin launcher: it runs the ordinary Go Eat server as a sidecar on a
//! loopback port, then points one webview window at it. The Go app needs no
//! Tauri API - the page is a remote origin with no capabilities - so this file
//! is the whole desktop integration. See spec/commit-plan-phase14-09-28-26.md.

// No console window behind the app in release builds on Windows.
#![cfg_attr(not(debug_assertions), windows_subsystem = "windows")]

use std::fs::{self, File, OpenOptions};
use std::io::Write;
use std::path::{Path, PathBuf};
use std::sync::atomic::{AtomicU16, Ordering};
use std::sync::{Arc, Mutex};

use tauri::{AppHandle, Manager, RunEvent, Url, WebviewUrl, WebviewWindow, WebviewWindowBuilder};
use tauri_plugin_opener::OpenerExt;
use tauri_plugin_shell::process::{CommandChild, CommandEvent};
use tauri_plugin_shell::ShellExt;

/// The one stdout line the Go server prints once it's listening
/// (web.ListeningPrefix): "GOEAT_LISTENING http://127.0.0.1:<port>".
const LISTENING_PREFIX: &str = "GOEAT_LISTENING ";

/// A file with this name next to the executable switches on portable mode:
/// everything is kept in a `data` folder beside it instead of the per-user
/// app-data folder. The Windows portable zip ships with it.
const PORTABLE_MARKER: &str = "portable.txt";

/// The running sidecar, killed on exit.
struct Sidecar(Mutex<Option<CommandChild>>);

fn main() {
    tauri::Builder::default()
        .plugin(tauri_plugin_shell::init())
        .plugin(tauri_plugin_opener::init())
        .manage(Sidecar(Mutex::new(None)))
        .setup(|app| {
            let handle = app.handle().clone();
            let port = Arc::new(AtomicU16::new(0));
            let (data_dir, portable) = resolve_data_dir(&handle)?;
            // In portable mode the webview's own profile (cookies, so the
            // sign-in) moves into the data folder too.
            let webview_dir = portable.then(|| data_dir.join("webview"));
            let window = build_window(&handle, port.clone(), webview_dir)?;
            let install = InstallInfo::detect(&handle, portable);
            if let Err(err) = start_server(&handle, &window, port, &data_dir, &install) {
                show_error(
                    &window,
                    &format!("Go Eat couldn't start its server.\n{err}"),
                );
            }
            Ok(())
        })
        .build(tauri::generate_context!())
        .expect("error while building Go Eat")
        .run(|app, event| {
            if let RunEvent::Exit = event {
                if let Some(child) = app.state::<Sidecar>().0.lock().unwrap().take() {
                    let _ = child.kill();
                }
            }
        });
}

/// The main window. It starts on the bundled splash page and is navigated to
/// the server once it announces its port. Navigation is fenced to the splash
/// and this server's own origin; any other http(s)/mailto link (a recipe's
/// source site, an attribution) opens in the system browser instead of
/// taking over the app window.
fn build_window(
    app: &AppHandle,
    port: Arc<AtomicU16>,
    webview_dir: Option<PathBuf>,
) -> tauri::Result<WebviewWindow> {
    let opener = app.clone();
    let mut builder = WebviewWindowBuilder::new(app, "main", WebviewUrl::App("index.html".into()));
    if let Some(dir) = webview_dir {
        builder = builder.data_directory(dir);
    }
    builder
        .title("Go Eat")
        .inner_size(1280.0, 860.0)
        .min_inner_size(800.0, 600.0)
        .on_navigation(move |url| {
            if is_app_url(url, port.load(Ordering::SeqCst)) {
                return true;
            }
            if matches!(url.scheme(), "http" | "https" | "mailto") {
                let _ = opener.opener().open_url(url.as_str(), None::<&str>);
            }
            false
        })
        .build()
}

/// The bundled splash (tauri://localhost, or http(s)://tauri.localhost on
/// Windows) or the sidecar's exact loopback origin.
fn is_app_url(url: &Url, port: u16) -> bool {
    if url.scheme() == "tauri" || url.host_str() == Some("tauri.localhost") {
        return true;
    }
    port != 0
        && url.scheme() == "http"
        && url.host_str() == Some("127.0.0.1")
        && url.port() == Some(port)
}

/// Where Go Eat keeps its data, and whether that's portable mode: a `data`
/// folder next to the executable when portable.txt sits beside it, otherwise
/// the per-user app-data folder (%APPDATA%\com.goeat.desktop,
/// ~/.local/share/com.goeat.desktop).
fn resolve_data_dir(app: &AppHandle) -> Result<(PathBuf, bool), Box<dyn std::error::Error>> {
    let exe_dir = std::env::current_exe()?
        .parent()
        .map(Path::to_path_buf)
        .ok_or("executable has no parent directory")?;
    let (dir, portable) = if exe_dir.join(PORTABLE_MARKER).is_file() {
        (exe_dir.join("data"), true)
    } else {
        (app.path().app_data_dir()?, false)
    };
    fs::create_dir_all(&dir)?;
    Ok((dir, portable))
}

/// What the Go sidecar needs to install an update from About -> Updates
/// (spec/commit-plan-phase15-09-30-26.md): only this shell knows where its
/// bundled updater is, how it was installed, and its own process, which the
/// updater stops and relaunches.
struct InstallInfo {
    /// The branded QUpdateTool updater CI bundled as a resource; empty when
    /// this build has none (a local build).
    updater: String,
    /// installer | portable | deb | appimage | dev (see config/desktop.go).
    kind: &'static str,
    /// What the updater relaunches: this exe, or for an AppImage the
    /// .AppImage file rather than the binary inside its read-only mount.
    exe: String,
}

impl InstallInfo {
    fn detect(app: &AppHandle, portable: bool) -> Self {
        let exe = std::env::current_exe()
            .map(|p| p.to_string_lossy().into_owned())
            .unwrap_or_default();
        let appimage = std::env::var("APPIMAGE").unwrap_or_default();

        let kind = if cfg!(debug_assertions) {
            "dev"
        } else if cfg!(windows) {
            if portable {
                "portable"
            } else {
                "installer"
            }
        } else if !appimage.is_empty() {
            "appimage"
        } else {
            "deb"
        };

        let name = if cfg!(windows) {
            "updater.exe"
        } else {
            "updater"
        };
        let updater = app
            .path()
            .resource_dir()
            .map(|dir| dir.join(name))
            .ok()
            .filter(|p| p.is_file())
            .map(|p| p.to_string_lossy().into_owned())
            .unwrap_or_default();

        let exe = if kind == "appimage" { appimage } else { exe };
        InstallInfo { updater, kind, exe }
    }
}

/// Starts the Go server with everything it keeps in data_dir, then relays its
/// output: the announce line navigates the window, the rest goes to goeat.log
/// next to the database.
fn start_server(
    app: &AppHandle,
    window: &WebviewWindow,
    port: Arc<AtomicU16>,
    data_dir: &Path,
    install: &InstallInfo,
) -> Result<(), String> {
    let session_secret = load_or_create_secret(&data_dir.join("session.key"))?;
    // Fresh every launch and only honoured while no account exists, so it's
    // worthless once setup is done (QSS security design §15).
    let setup_token = random_hex(16)?;
    let path = |name: &str| data_dir.join(name).to_string_lossy().into_owned();

    let mut log = OpenOptions::new()
        .create(true)
        .write(true)
        .truncate(true)
        .open(data_dir.join("goeat.log"))
        .map_err(|e| format!("open log: {e}"))?;

    let (mut rx, child) = app
        .shell()
        .sidecar("goeat")
        .map_err(|e| e.to_string())?
        .current_dir(data_dir)
        .envs([
            ("GOEAT_DESKTOP", "1".to_string()),
            ("LISTEN_ADDR", "127.0.0.1:0".to_string()),
            ("DATABASE_URL", format!("file:{}", path("goeat.db"))),
            ("RECIPE_IMAGE_DIR", path("recipe-images")),
            ("ITEM_IMAGE_DIR", path("item-images")),
            ("SESSION_SECRET", session_secret),
            ("SETUP_TOKEN", setup_token.clone()),
            // Installing updates (About -> Updates -> Install).
            ("GOEAT_UPDATER", install.updater.clone()),
            ("GOEAT_INSTALL_KIND", install.kind.to_string()),
            ("GOEAT_SHELL_PID", std::process::id().to_string()),
            ("GOEAT_SHELL_EXE", install.exe.clone()),
            ("GOEAT_DATA_DIR", data_dir.to_string_lossy().into_owned()),
        ])
        .spawn()
        .map_err(|e| format!("spawn goeat: {e}"))?;
    *app.state::<Sidecar>().0.lock().unwrap() = Some(child);

    let window = window.clone();
    let log_path = data_dir.join("goeat.log");
    tauri::async_runtime::spawn(async move {
        let mut started = false;
        while let Some(event) = rx.recv().await {
            match event {
                CommandEvent::Stdout(bytes) => {
                    let line = String::from_utf8_lossy(&bytes);
                    if let Some(base) = line.trim().strip_prefix(LISTENING_PREFIX) {
                        if let Ok(url) = Url::parse(base) {
                            port.store(url.port().unwrap_or(0), Ordering::SeqCst);
                            // /setup redirects on to the app once an account
                            // exists; on first run the wizard reads the token
                            // from the fragment, which never reaches the server.
                            if let Ok(target) = url.join(&format!("/setup#token={setup_token}")) {
                                started = true;
                                let _ = window.navigate(target);
                            }
                        }
                    }
                    let _ = log.write_all(&bytes);
                }
                CommandEvent::Stderr(bytes) => {
                    let _ = log.write_all(&bytes);
                }
                CommandEvent::Terminated(status) => {
                    let _ = writeln!(log, "goeat exited: {:?}", status.code);
                    let what = if started {
                        "stopped unexpectedly"
                    } else {
                        "failed to start"
                    };
                    show_error(
                        &window,
                        &format!(
                            "Go Eat's server {what}.\nDetails are in {}",
                            log_path.display()
                        ),
                    );
                    break;
                }
                _ => {}
            }
        }
    });
    Ok(())
}

/// Sends the window back to the splash page with msg shown as an error.
fn show_error(window: &WebviewWindow, msg: &str) {
    let mut url = splash_url();
    url.query_pairs_mut().append_pair("error", msg);
    let _ = window.navigate(url);
}

/// Where the bundled splash page lives: Tauri serves app assets from
/// http://tauri.localhost on Windows and tauri://localhost elsewhere.
fn splash_url() -> Url {
    let base = if cfg!(windows) {
        "http://tauri.localhost/index.html"
    } else {
        "tauri://localhost/index.html"
    };
    Url::parse(base).expect("static splash URL")
}

/// The server's SESSION_SECRET, kept across launches so sign-ins and the
/// encrypted secrets it seals (the Home Assistant token) survive a restart.
fn load_or_create_secret(path: &Path) -> Result<String, String> {
    if let Ok(existing) = fs::read_to_string(path) {
        let existing = existing.trim().to_string();
        if existing.len() >= 64 {
            return Ok(existing);
        }
    }
    let secret = random_hex(32)?;
    write_private(path, &secret).map_err(|e| format!("write {}: {e}", path.display()))?;
    Ok(secret)
}

/// Writes a file only the current user can read (0600 on Linux; on Windows
/// the per-user AppData directory's ACL already restricts it).
fn write_private(path: &Path, contents: &str) -> std::io::Result<()> {
    let mut opts = OpenOptions::new();
    opts.create(true).write(true).truncate(true);
    #[cfg(unix)]
    {
        use std::os::unix::fs::OpenOptionsExt;
        opts.mode(0o600);
    }
    let mut f: File = opts.open(path)?;
    f.write_all(contents.as_bytes())
}

fn random_hex(bytes: usize) -> Result<String, String> {
    let mut buf = vec![0u8; bytes];
    getrandom::fill(&mut buf).map_err(|e| format!("random: {e}"))?;
    Ok(buf.iter().map(|b| format!("{b:02x}")).collect())
}
