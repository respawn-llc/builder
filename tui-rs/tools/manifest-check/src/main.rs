#![forbid(unsafe_code)]

use std::path::PathBuf;

fn main() {
    let args = std::env::args().skip(1).collect::<Vec<_>>();
    let parsed_args = match parse_args(&args) {
        Ok(parsed_args) => parsed_args,
        Err(message) => {
            eprintln!("{message}");
            std::process::exit(2);
        }
    };
    let status = match parsed_args.command.as_str() {
        "check" => manifest_check::check_repository(&parsed_args.repo_root),
        "lint" => manifest_check::lint_policy::check(&parsed_args.repo_root),
        command => Err(vec![manifest_check::Finding {
            code: "manifest_check_unknown_command",
            path: None,
            detail: format!("unknown command {command}"),
        }]),
    };
    if let Err(findings) = status {
        for finding in findings {
            eprintln!("{finding}");
        }
        std::process::exit(1);
    }
}

struct ParsedArgs {
    command: String,
    repo_root: PathBuf,
}

fn parse_args(args: &[String]) -> Result<ParsedArgs, String> {
    let mut command = "check".to_owned();
    let mut repo_root = PathBuf::from(".");
    let mut index = 0_usize;
    while index < args.len() {
        let arg = &args[index];
        if arg == "--repo-root" {
            let Some(value) = args.get(index + 1) else {
                return Err("--repo-root requires a value".to_owned());
            };
            repo_root = PathBuf::from(value);
            index += 2;
            continue;
        }
        if arg.starts_with("--") {
            return Err(format!("unknown option {arg}"));
        }
        if matches!(arg.as_str(), "check" | "lint") {
            command = arg.clone();
            index += 1;
            continue;
        }
        return Err(format!("unknown command {arg}"));
    }
    Ok(ParsedArgs { command, repo_root })
}
