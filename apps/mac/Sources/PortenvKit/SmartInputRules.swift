// SPDX-License-Identifier: Apache-2.0

import Foundation

/// ADR 0027 §4 step 3: the built-in rules, on the Mac, always present and
/// the default classifier. A line is `shell` if it starts with a known
/// program or a path, or holds shell syntax; `english` only on strong signs
/// (several words, no shell syntax, and a question mark or a request verb);
/// anything else is unsure (nil). Lines the rules call shell never reach a
/// hosted classifier.
public struct RuleClassifier: Classifier {
    public let id = "rules"
    private let programs: Set<String>

    /// `programs`: the known first words. S2 adds the spec set's names and
    /// history's first words; until then, shell builtins and common tools.
    public init(programs: Set<String> = RuleClassifier.builtinPrograms) { self.programs = programs }

    public static let builtinPrograms: Set<String> = [
        // Shell builtins and keywords.
        "alias", "bg", "bind", "break", "builtin", "case", "cd", "command", "compgen", "complete", "continue", "declare",
        "dirs", "disown", "echo", "enable", "eval", "exec", "exit", "export", "false", "fc", "fg", "for", "function",
        "getopts", "hash", "help", "history", "if", "jobs", "kill", "let", "local", "logout", "popd", "printf", "pushd",
        "pwd", "read", "readonly", "return", "set", "shift", "shopt", "source", "suspend", "test", "time", "times",
        "trap", "true", "type", "typeset", "ulimit", "umask", "unalias", "unset", "until", "wait", "while", ".",
        // Common programs.
        "apt", "apt-get", "awk", "bash", "brew", "bun", "cargo", "cat", "chmod", "chown", "clear", "cp", "curl", "cut",
        "date", "deno", "df", "diff", "dig", "docker", "du", "env", "fish", "find", "gcc", "gh", "git", "go", "grep",
        "gzip", "head", "htop", "jq", "kubectl", "less", "ln", "ls", "make", "man", "mkdir", "more", "mv", "nano",
        "node", "npm", "npx", "nvim", "ping", "pip", "pip3", "pnpm", "portenv", "ps", "python", "python3", "rg", "rm",
        "rmdir", "rsync", "ruby", "scp", "sed", "sh", "sort", "ssh", "sudo", "swift", "tail", "tar", "tee", "terraform",
        "tmux", "top", "touch", "tr", "uniq", "unzip", "vi", "vim", "watch", "wc", "wget", "which", "xargs", "yarn",
        "zip", "zsh",
    ]

    /// Words a request starts with.
    static let requestVerbs: Set<String> = [
        "please", "show", "list", "find", "create", "start", "stop", "restart", "wake", "deploy", "open", "delete",
        "remove", "give", "tell", "explain", "how", "what", "which", "why", "where", "when", "can", "could", "would",
        "should", "is", "are", "add", "build", "install", "update", "upgrade", "send", "message", "ask", "check",
        "count", "search", "rename", "undo", "revert", "kill", "free", "clean", "compress", "download", "copy",
    ]

    public func classify(_ c: InputContext) async throws -> (Classification?, AdapterUsage) {
        (decide(c.line).map { Classification(intent: $0, confidence: 1) }, AdapterUsage())
    }

    /// The rule, alone.
    func decide(_ raw: String) -> Intent? {
        let line = raw.trimmingCharacters(in: .whitespacesAndNewlines)
        guard let first = line.split(whereSeparator: \.isWhitespace).first.map(String.init) else { return nil }
        if programs.contains(first) || first.hasPrefix("/") || first.hasPrefix("./") || first.hasPrefix("../") || first.hasPrefix("~") {
            return .shell
        }
        if Self.hasShellSyntax(line) { return .shell }
        let words = line.split(whereSeparator: \.isWhitespace)
        guard words.count >= 3 else { return nil }
        if line.hasSuffix("?") || Self.requestVerbs.contains(first.lowercased()) { return .english }
        return nil
    }

    /// Pipes, redirects, `&&`, `;`, assignments, quotes, substitutions. A
    /// quote counts only as a pair: one apostrophe is English ("what's").
    static func hasShellSyntax(_ line: String) -> Bool {
        if line.contains(where: { "|<>;`$&".contains($0) }) { return true }
        for q: Character in ["\"", "'"] where line.filter({ $0 == q }).count >= 2 { return true }
        // An assignment at the start: NAME=value.
        if line.firstMatch(of: /^[A-Za-z_][A-Za-z0-9_]*=/) != nil { return true }
        return false
    }
}
