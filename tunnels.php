<?php
/**
 * SSH-туннели — локальная веб-панель в одном файле.
 *
 * Запуск:   php tunnels.php            (откроет браузер сам)
 *           php tunnels.php 9000       (другой порт панели)
 *           php tunnels.php --no-open  (не открывать браузер)
 *
 * Данные лежат рядом, в папке tunnels-data/ (список туннелей, логи).
 */

const DEFAULT_PORT = 8099;
define('IS_WIN', PHP_OS_FAMILY === 'Windows');
define('DATA', __DIR__ . DIRECTORY_SEPARATOR . 'tunnels-data');

/* ============================ Запуск из консоли ============================ */
if (PHP_SAPI === 'cli') {
    $port = DEFAULT_PORT;
    foreach (array_slice($argv, 1) as $a) if (ctype_digit($a)) $port = (int)$a;
    $url = "http://127.0.0.1:$port/";
    echo "SSH-туннели: $url\nCtrl+C останавливает панель, сами туннели продолжают работать.\n";
    if (!IS_WIN) putenv('PHP_CLI_SERVER_WORKERS=6');
    if (!in_array('--no-open', $argv, true)) {
        if (IS_WIN)      $h = popen('start /B cmd /c "ping -n 2 127.0.0.1 >nul & start "" "' . $url . '""', 'r');
        elseif (PHP_OS_FAMILY === 'Darwin') shell_exec('(sleep 1; open ' . escapeshellarg($url) . ') >/dev/null 2>&1 &');
        else             shell_exec('(sleep 1; xdg-open ' . escapeshellarg($url) . ') >/dev/null 2>&1 &');
    }
    passthru(escapeshellarg(PHP_BINARY) . ' -S 127.0.0.1:' . $port . ' ' . escapeshellarg(__FILE__));
    exit;
}

/* ============================ Защита ============================ */
// Панель умеет запускать процессы, поэтому: только localhost (против DNS-rebinding)
if (!preg_match('/^(127\.0\.0\.1|localhost)(:\d+)?$/i', $_SERVER['HTTP_HOST'] ?? '')) { http_response_code(403); exit('forbidden'); }

$path = parse_url($_SERVER['REQUEST_URI'] ?? '/', PHP_URL_PATH);
$api  = $_GET['api'] ?? null;
if ($api === null) {
    if ($path !== '/' && $path !== '/index.php') { http_response_code(404); exit; }
    header('Content-Type: text/html; charset=utf-8');
    $fh = fopen(__FILE__, 'r'); fseek($fh, __COMPILER_HALT_OFFSET__); fpassthru($fh); fclose($fh);
    exit;
}
// и только свой фронтенд (кастомный заголовок нельзя отправить с чужого сайта без CORS)
if (($_SERVER['HTTP_X_TUNNELS'] ?? '') !== '1') jout(['error' => 'bad request'], 400);

/* ============================ Хранилище ============================ */
if (!is_dir(DATA)) @mkdir(DATA, 0700, true);
if (!is_dir(DATA . '/logs')) @mkdir(DATA . '/logs', 0700, true);

function jout($d, $c = 200) {
    http_response_code($c);
    header('Content-Type: application/json; charset=utf-8');
    header('Cache-Control: no-store');
    echo json_encode($d, JSON_UNESCAPED_UNICODE | JSON_UNESCAPED_SLASHES | JSON_INVALID_UTF8_SUBSTITUTE);
    exit;
}
function load_json($n) {
    $f = DATA . "/$n.json";
    if (!is_file($f)) return [];
    $d = json_decode((string)file_get_contents($f), true);
    return is_array($d) ? $d : [];
}
function save_json($n, $d) {
    $f = DATA . "/$n.json";
    file_put_contents($f, json_encode($d, JSON_PRETTY_PRINT | JSON_UNESCAPED_UNICODE | JSON_UNESCAPED_SLASHES), LOCK_EX);
    @chmod($f, 0600);
}
function locked(callable $fn) {
    $h = fopen(DATA . '/.lock', 'c');
    flock($h, LOCK_EX);
    try { return $fn(); } finally { flock($h, LOCK_UN); fclose($h); }
}

/* ============================ Разбор ssh-команды ============================ */
function tokenize($s) {
    preg_match_all('/"([^"]*)"|\'([^\']*)\'|(\S+)/u', $s, $m, PREG_SET_ORDER);
    $t = [];
    foreach ($m as $x) {
        if (isset($x[3]) && $x[3] !== '') $t[] = $x[3];
        elseif (isset($x[2]) && $x[2] !== '') $t[] = $x[2];
        else $t[] = $x[1];
    }
    return $t;
}

/** ssh -f -N -L 1111:11.11.1.11:222 login@host -p22  →  структура */
function parse_cmd($cmd) {
    $t = tokenize(trim($cmd));
    if ($t && preg_match('~(^|[\\\\/])ssh(\.exe)?$~i', $t[0])) array_shift($t);
    $withArg = str_split('bcDEeFIiJLlmOoQRSWw' . 'p');
    $drop = str_split('fNTn');
    $r = ['user' => '', 'host' => '', 'port' => '', 'forwards' => [], 'extra' => [], 'lport' => 0, 'error' => null];
    $pos = [];
    for ($i = 0; $i < count($t); $i++) {
        $tok = $t[$i];
        if ($tok === '--') { $pos = array_merge($pos, array_slice($t, $i + 1)); break; }
        if ($tok[0] !== '-' || strlen($tok) < 2) { $pos[] = $tok; continue; }
        for ($j = 1; $j < strlen($tok); $j++) {
            $c = $tok[$j];
            if (in_array($c, $withArg, true)) {
                $arg = substr($tok, $j + 1);
                if ($arg === '' || $arg === false) { $arg = $t[++$i] ?? ''; }
                if ($c === 'L') $r['forwards'][] = $arg;
                elseif ($c === 'p') $r['port'] = $arg;
                elseif ($c === 'l') $r['user'] = $arg;
                else { $r['extra'][] = "-$c"; $r['extra'][] = $arg; }
                break;
            }
            if (!in_array($c, $drop, true)) $r['extra'][] = "-$c";
        }
    }
    if (!$pos) { $r['error'] = 'Не нашёл user@host в команде'; return $r; }
    $dest = $pos[0];
    if (($at = strrpos($dest, '@')) !== false) { $r['user'] = substr($dest, 0, $at); $dest = substr($dest, $at + 1); }
    $r['host'] = $dest;
    if ($r['port'] !== '' && !ctype_digit($r['port'])) { $r['error'] = 'Некорректный порт -p'; return $r; }
    if (!$r['forwards']) { $r['error'] = 'Не нашёл -L (проброс порта)'; return $r; }
    $parts = explode(':', $r['forwards'][0]);
    $lp = count($parts) >= 4 ? $parts[1] : $parts[0];
    if (!ctype_digit($lp)) { $r['error'] = 'Не понял локальный порт в -L'; return $r; }
    $r['lport'] = (int)$lp;
    $r['target'] = count($parts) >= 4 ? "{$parts[2]}:{$parts[3]}" : implode(':', array_slice($parts, 1));
    return $r;
}

function ssh_args($p, $withPassword) {
    $a = $p['extra'];  // пользовательские -o и т.п. идут первыми — у ssh побеждает первое значение
    array_push($a, '-N', '-o', 'ExitOnForwardFailure=yes', '-o', 'StrictHostKeyChecking=accept-new',
        '-o', 'ServerAliveInterval=30', '-o', 'ServerAliveCountMax=3', '-o', 'ConnectTimeout=15');
    if ($withPassword) array_push($a, '-o', 'NumberOfPasswordPrompts=1', '-o', 'PreferredAuthentications=password,keyboard-interactive');
    foreach ($p['forwards'] as $f) array_push($a, '-L', $f);
    if ($p['port'] !== '') array_push($a, '-p', $p['port']);
    if ($p['user'] !== '') array_push($a, '-l', $p['user']);
    $a[] = $p['host'];
    return $a;
}

/* ============================ Процессы ============================ */
function port_open($port) {
    $f = @fsockopen('127.0.0.1', $port, $en, $es, 0.25);
    if ($f) { fclose($f); return true; }
    return false;
}
function pid_alive($pid) {
    $pid = (int)$pid;
    if ($pid <= 0) return false;
    if (IS_WIN) return stripos((string)shell_exec('tasklist /FI "PID eq ' . $pid . '" /NH /FO CSV 2>NUL'), 'ssh') !== false;
    if (is_dir('/proc')) return is_dir("/proc/$pid");
    return function_exists('posix_kill') ? @posix_kill($pid, 0) : trim((string)shell_exec('ps -p ' . $pid . ' -o pid= 2>/dev/null')) !== '';
}
function kill_pid($pid) {
    $pid = (int)$pid;
    if ($pid <= 0) return;
    if (IS_WIN) shell_exec('taskkill /PID ' . $pid . ' /T /F >NUL 2>&1');
    else shell_exec('kill ' . $pid . ' 2>/dev/null');
}
/** Туннель, запущенный вручную / панелью раньше: ищем ssh, слушающий порт */
function find_pid_by_port($port) {
    $pid = 0;
    if (IS_WIN) {
        $out = (string)shell_exec('netstat -ano -p tcp 2>NUL');
        if (preg_match('/^\s*TCP\s+(?:127\.0\.0\.1|\[::1\]|0\.0\.0\.0):' . (int)$port . '\s+\S+\s+LISTENING\s+(\d+)/mi', $out, $m)) $pid = (int)$m[1];
        if ($pid && stripos((string)shell_exec('tasklist /FI "PID eq ' . $pid . '" /NH /FO CSV 2>NUL'), 'ssh') === false) $pid = 0;
    } else {
        $out = trim((string)shell_exec('lsof -ti tcp:' . (int)$port . ' -sTCP:LISTEN 2>/dev/null | head -1'));
        if ($out === '') {
            $ss = (string)shell_exec('ss -ltnp 2>/dev/null');
            if (preg_match('/:' . (int)$port . '\s.*?pid=(\d+)/', $ss, $m)) $out = $m[1];
        }
        $pid = (int)$out;
        if ($pid && stripos((string)shell_exec('ps -p ' . $pid . ' -o comm= 2>/dev/null'), 'ssh') === false) $pid = 0;
    }
    return $pid;
}

function askpass_file() {
    if (IS_WIN) {
        $f = DATA . '\\askpass.cmd';
        file_put_contents($f, "@echo off\r\nsetlocal EnableDelayedExpansion\r\necho(!SSH_PW!\r\n");
    } else {
        $f = DATA . '/askpass.sh';
        file_put_contents($f, "#!/bin/sh\nprintf '%s\\n' \"\$SSH_PW\"\n");
        chmod($f, 0700);
    }
    return $f;
}

function log_file($id) { return DATA . DIRECTORY_SEPARATOR . 'logs' . DIRECTORY_SEPARATOR . $id . '.log'; }
function log_tail($id) {
    $f = log_file($id);
    $s = is_file($f) ? (string)file_get_contents($f) : '';
    if (IS_WIN && is_file("$f.out")) $s .= (string)file_get_contents("$f.out");
    $lines = array_filter(array_map('trim', preg_split('/\R/', $s)), fn($l) => $l !== '' && stripos($l, 'Permanently added') === false);
    return implode("\n", array_slice($lines, -4));
}

function launch($t, $p) {
    $id = $t['id'];
    $log = log_file($id);
    @unlink($log); @unlink("$log.out");
    $ssh = getenv('SSH_BIN') ?: 'ssh';
    $args = ssh_args($p, $t['password'] !== '');

    putenv('SSH_ASKPASS=' . askpass_file());
    putenv('SSH_ASKPASS_REQUIRE=force');
    if (!getenv('DISPLAY')) putenv('DISPLAY=localhost:0');
    putenv('SSH_PW=' . $t['password']);

    if (IS_WIN) {
        $q = fn($s) => "'" . str_replace("'", "''", $s) . "'";
        $argList = array_map(fn($a) => preg_match('/[\s"]/', $a) ? '"' . str_replace('"', '\"', $a) . '"' : $a, $args);
        $ps = '$ErrorActionPreference="Stop"; $p = Start-Process -FilePath ' . $q($ssh)
            . ' -ArgumentList @(' . implode(',', array_map($q, $argList)) . ')'
            . ' -WindowStyle Hidden -PassThru -RedirectStandardError ' . $q($log) . ' -RedirectStandardOutput ' . $q($log . '.out')
            . '; $p.Id';
        $enc = base64_encode(function_exists('mb_convert_encoding') ? mb_convert_encoding($ps, 'UTF-16LE', 'UTF-8') : iconv('UTF-8', 'UTF-16LE', $ps));
        $out = (string)shell_exec('powershell -NoProfile -NonInteractive -ExecutionPolicy Bypass -EncodedCommand ' . $enc . ' 2>&1');
        preg_match('/(\d+)\s*$/', trim($out), $m);
        $pid = (int)($m[1] ?? 0);
        if (!$pid) $err = trim($out);
    } else {
        $setsid = trim((string)shell_exec('command -v setsid 2>/dev/null')) !== '' ? 'setsid ' : '';
        $cmd = 'cd / && nohup ' . $setsid . implode(' ', array_map('escapeshellarg', array_merge([$ssh], $args)))
            . ' >' . escapeshellarg($log) . ' 2>&1 </dev/null & echo $!';
        $pid = (int)trim((string)shell_exec($cmd));
        if (!$pid) $err = 'Не удалось запустить ssh';
    }
    putenv('SSH_PW'); // убрать пароль из окружения панели
    if (!$pid) return ['ok' => false, 'error' => $err ?? 'Не удалось запустить ssh'];
    locked(function () use ($id, $pid) {
        $st = load_json('state');
        $st[$id] = ['pid' => $pid, 'started' => time()];
        save_json('state', $st);
    });
    return ['ok' => true];
}

function view_of($t, $st) {
    $p = parse_cmd($t['command']);
    $lport = $p['lport'];
    $v = [
        'id' => $t['id'], 'name' => $t['name'], 'command' => $t['command'],
        'hasPassword' => $t['password'] !== '',
        'url' => $t['url'] !== '' ? $t['url'] : ($lport ? "http://localhost:$lport" : ''),
        'urlCustom' => $t['url'] !== '',
        'lport' => $lport, 'host' => $p['host'], 'user' => $p['user'], 'sshPort' => $p['port'] ?: '22',
        'target' => $p['target'] ?? '', 'status' => 'down', 'error' => $p['error'], 'external' => false,
    ];
    if ($p['error']) { $v['status'] = 'invalid'; return $v; }
    if (port_open($lport)) { $v['status'] = 'up'; $v['external'] = !$st; return $v; }
    if ($st) {
        if (pid_alive($st['pid'])) { $v['status'] = 'connecting'; return $v; }
        $v['status'] = 'failed';
        $v['error'] = log_tail($t['id']) ?: 'Процесс ssh завершился без сообщения';
    }
    return $v;
}

function find_t($id) {
    foreach (load_json('tunnels') as $t) if ($t['id'] === $id) return $t;
    return null;
}

function stop_tunnel($t) {
    $p = parse_cmd($t['command']);
    $st = load_json('state');
    $pid = $st[$t['id']]['pid'] ?? 0;
    if ($pid) kill_pid($pid);
    elseif (!$p['error'] && port_open($p['lport']) && ($ep = find_pid_by_port($p['lport']))) kill_pid($ep);
    locked(function () use ($t) {
        $st = load_json('state'); unset($st[$t['id']]); save_json('state', $st);
    });
}

function start_tunnel($t) {
    $p = parse_cmd($t['command']);
    if ($p['error']) return ['ok' => false, 'error' => $p['error']];
    if (port_open($p['lport'])) return ['ok' => true];
    $st = load_json('state');
    if (isset($st[$t['id']]) && pid_alive($st[$t['id']]['pid'])) return ['ok' => true];
    return launch($t, $p);
}

function norm_url($u) {
    $u = trim($u);
    if ($u !== '' && !preg_match('~^[a-z][a-z0-9+.-]*://~i', $u)) $u = 'http://' . $u;
    return $u;
}

/* ============================ API ============================ */
$in = json_decode((string)file_get_contents('php://input'), true) ?: [];
$id = (string)($in['id'] ?? '');

switch ($api) {
    case 'list':
        $st = load_json('state');
        $out = [];
        foreach (load_json('tunnels') as $t) $out[] = view_of($t, $st[$t['id']] ?? null);
        jout(['tunnels' => $out]);

    case 'parse':
        $p = parse_cmd((string)($in['command'] ?? ''));
        jout($p['error'] ? ['error' => $p['error']] : [
            'host' => $p['host'], 'user' => $p['user'], 'sshPort' => $p['port'] ?: '22', 'lport' => $p['lport'], 'target' => $p['target'],
        ]);

    case 'get':   // данные для формы редактирования (с паролем)
        $t = find_t($id) or jout(['error' => 'not found'], 404);
        jout($t);

    case 'password':
        $t = find_t($id) or jout(['error' => 'not found'], 404);
        jout(['password' => $t['password']]);

    case 'save':
        $cmd = trim((string)($in['command'] ?? ''));
        $p = parse_cmd($cmd);
        if ($p['error']) jout(['error' => $p['error']], 422);
        $rec = [
            'name' => trim((string)($in['name'] ?? '')) ?: ($p['user'] ? "{$p['user']}@" : '') . $p['host'] . ':' . $p['lport'],
            'command' => $cmd,
            'password' => (string)($in['password'] ?? ''),
            'url' => norm_url((string)($in['url'] ?? '')),
        ];
        locked(function () use ($id, $rec) {
            $all = load_json('tunnels');
            $done = false;
            foreach ($all as &$t) if ($id !== '' && $t['id'] === $id) { $t = ['id' => $id] + $rec; $done = true; }
            unset($t);
            if (!$done) $all[] = ['id' => bin2hex(random_bytes(4))] + $rec;
            save_json('tunnels', $all);
        });
        jout(['ok' => true]);

    case 'delete':
        if ($t = find_t($id)) stop_tunnel($t);
        locked(function () use ($id) {
            save_json('tunnels', array_values(array_filter(load_json('tunnels'), fn($t) => $t['id'] !== $id)));
        });
        jout(['ok' => true]);

    case 'up':
        $t = find_t($id) or jout(['error' => 'not found'], 404);
        jout(start_tunnel($t));

    case 'down':
        $t = find_t($id) or jout(['error' => 'not found'], 404);
        stop_tunnel($t);
        jout(['ok' => true]);

    case 'upall':
        $errs = [];
        foreach (load_json('tunnels') as $t) { $r = start_tunnel($t); if (!$r['ok']) $errs[] = $t['name'] . ': ' . $r['error']; }
        jout(['ok' => !$errs, 'error' => implode("\n", $errs)]);

    case 'downall':
        foreach (load_json('tunnels') as $t) stop_tunnel($t);
        jout(['ok' => true]);

    case 'import':
        // по строке на туннель:  команда   или   название | команда | пароль | ссылка
        $added = 0; $bad = [];
        locked(function () use ($in, &$added, &$bad) {
            $all = load_json('tunnels');
            foreach (preg_split('/\R/', (string)($in['text'] ?? '')) as $line) {
                $line = trim($line);
                if ($line === '') continue;
                $f = array_map('trim', explode('|', $line));
                if (count($f) >= 2) [$name, $cmd, $pw, $url] = array_pad($f, 4, ''); else { $name = $pw = $url = ''; $cmd = $f[0]; }
                $p = parse_cmd($cmd);
                if ($p['error']) { $bad[] = "$line — {$p['error']}"; continue; }
                $all[] = ['id' => bin2hex(random_bytes(4)),
                    'name' => $name ?: ($p['user'] ? "{$p['user']}@" : '') . $p['host'] . ':' . $p['lport'],
                    'command' => $cmd, 'password' => $pw, 'url' => norm_url($url)];
                $added++;
            }
            save_json('tunnels', $all);
        });
        jout(['added' => $added, 'bad' => $bad]);
}
jout(['error' => 'unknown api'], 404);

__halt_compiler();<!doctype html>
<html lang="ru">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>SSH-туннели</title>
<style>
:root{--bg:#0f1115;--panel:#171a21;--line:#262b36;--tx:#e6e9ef;--mut:#8a93a6;--acc:#4c8dff;--ok:#2ecc71;--warn:#f5a524;--bad:#ff5d5d}
*{box-sizing:border-box}
body{margin:0;background:var(--bg);color:var(--tx);font:14px/1.45 system-ui,-apple-system,"Segoe UI",sans-serif}
.wrap{max-width:1060px;margin:0 auto;padding:28px 20px 80px}
header{display:flex;align-items:center;gap:8px;flex-wrap:wrap;margin-bottom:18px}
h1{font-size:20px;margin:0 auto 0 0;font-weight:650}
h1 small{color:var(--mut);font-weight:400;font-size:13px;margin-left:8px}
button{font:inherit;color:var(--tx);background:#222733;border:1px solid var(--line);border-radius:8px;padding:7px 13px;cursor:pointer}
button:hover{background:#2a3040}
button:disabled{opacity:.5;cursor:default}
button.pri{background:var(--acc);border-color:var(--acc);color:#fff}
button.pri:hover{filter:brightness(1.1)}
button.ok{background:var(--ok);border-color:var(--ok);color:#06210f;font-weight:600}
button.bad{color:var(--bad)}
button.ghost{background:transparent;border-color:transparent;color:var(--mut);padding:7px 9px}
button.ghost:hover{color:var(--tx);background:#222733}
input,textarea{font:inherit;color:var(--tx);background:#0f1218;border:1px solid var(--line);border-radius:8px;padding:8px 10px;width:100%}
input:focus,textarea:focus{outline:2px solid var(--acc);outline-offset:-1px}
textarea{font-family:ui-monospace,Consolas,monospace;font-size:13px;resize:vertical}
#q{max-width:170px}
.list{display:flex;flex-direction:column;gap:8px}
.row{background:var(--panel);border:1px solid var(--line);border-radius:12px;padding:12px 14px;display:grid;grid-template-columns:14px 1fr auto;gap:12px;align-items:center}
.dot{width:10px;height:10px;border-radius:50%;background:#4a5163}
.up .dot{background:var(--ok);box-shadow:0 0 0 4px #2ecc7126}
.connecting .dot{background:var(--warn);animation:p 1s infinite}
.failed .dot,.invalid .dot{background:var(--bad)}
@keyframes p{50%{opacity:.3}}
.name{font-weight:600}
.sub{color:var(--mut);font-size:12.5px;font-family:ui-monospace,Consolas,monospace;overflow-wrap:anywhere}
.url{display:inline-block;margin-top:4px;color:var(--acc);font:600 15px/1.3 ui-monospace,Consolas,monospace;text-decoration:none;overflow-wrap:anywhere}
.url:hover{text-decoration:underline}
.go{display:inline-block;background:var(--ok);color:#000;font:650 14px/1 system-ui,-apple-system,"Segoe UI",sans-serif;padding:10px 18px;border-radius:8px;text-decoration:none;white-space:nowrap}
.go:hover{filter:brightness(1.1)}
.go:focus-visible{outline:2px solid var(--tx);outline-offset:2px}
.act{display:flex;gap:4px;align-items:center;flex-wrap:wrap;justify-content:flex-end}
.err{grid-column:2/4;color:var(--bad);font:12px ui-monospace,Consolas,monospace;white-space:pre-wrap;background:#ff5d5d12;border-radius:8px;padding:8px 10px}
.empty{color:var(--mut);text-align:center;padding:60px 0}
.badge{font-size:11px;color:var(--mut);border:1px solid var(--line);border-radius:6px;padding:0 6px;margin-left:6px}
dialog{background:var(--panel);color:var(--tx);border:1px solid var(--line);border-radius:14px;padding:22px;width:min(560px,94vw)}
dialog::backdrop{background:#000a}
dialog h2{margin:0 0 14px;font-size:17px}
label{display:block;color:var(--mut);font-size:12.5px;margin:12px 0 5px}
.hint{font-size:12.5px;margin-top:6px;min-height:18px;color:var(--mut)}
.hint.e{color:var(--bad)}.hint.g{color:var(--ok)}
.foot{display:flex;gap:8px;margin-top:18px}
.foot .sp{margin-right:auto}
.pw{display:flex;gap:6px}
#toast{position:fixed;left:50%;bottom:22px;transform:translateX(-50%);background:#222733;border:1px solid var(--line);padding:9px 16px;border-radius:10px;opacity:0;pointer-events:none;transition:.2s;white-space:pre-wrap;max-width:90vw}
#toast.on{opacity:1}
@media(max-width:640px){.row{grid-template-columns:14px 1fr}.act{grid-column:1/3;justify-content:flex-start}.err{grid-column:1/3}}
</style>
</head>
<body>
<div class="wrap">
  <header>
    <h1>SSH-туннели <small id="count"></small></h1>
    <input id="q" placeholder="Поиск…  ( / )" autocomplete="off">
    <button id="upall">Подключить все</button>
    <button id="downall" class="bad">Отключить все</button>
    <button id="imp">Импорт</button>
    <button id="add" class="pri">+ Добавить</button>
  </header>
  <div class="list" id="list"></div>
</div>

<dialog id="dlg">
  <form method="dialog" id="f" autocomplete="off">
    <h2 id="dtitle">Туннель</h2>
    <label>Команда (вставьте как есть из блокнота)</label>
    <textarea id="cmd" rows="2" placeholder="ssh -f -N -L 1111:11.11.1.11:222 login@333.333.33.33 -p22" spellcheck="false"></textarea>
    <div class="hint" id="hint"></div>
    <label>Название</label>
    <input id="name" placeholder="Например: CRM тест">
    <label>Пароль SSH (необязательно, если вход по ключу)</label>
    <div class="pw"><input id="pw" type="password" autocomplete="new-password"><button type="button" id="eye">Показать</button></div>
    <label>Ссылка для открытия</label>
    <input id="url" placeholder="по умолчанию http://localhost:ЛОКАЛЬНЫЙ_ПОРТ" spellcheck="false">
    <div class="foot">
      <button type="button" id="del" class="bad sp">Удалить</button>
      <button type="button" id="cancel">Отмена</button>
      <button type="submit" id="save" class="pri">Сохранить</button>
    </div>
  </form>
</dialog>

<dialog id="idlg">
  <form method="dialog" autocomplete="off">
    <h2>Импорт из блокнота</h2>
    <div class="hint" style="margin:0 0 8px">По строке на туннель. Либо просто команда, либо<br><code>название | команда | пароль | ссылка</code></div>
    <textarea id="itext" rows="9" spellcheck="false" placeholder="CRM | ssh -f -N -L 1111:11.11.1.11:222 login@333.333.33.33 -p22 | secret | localhost:222"></textarea>
    <div class="hint e" id="ihint"></div>
    <div class="foot"><span class="sp"></span><button type="button" id="icancel">Отмена</button><button type="submit" class="pri" id="idoit">Импортировать</button></div>
  </form>
</dialog>
<div id="toast"></div>

<script>
const $ = s => document.querySelector(s);
let data = [], editing = null, timer = null;

async function api(name, body) {
  const r = await fetch('?api=' + name, {method: 'POST', headers: {'X-Tunnels': '1', 'Content-Type': 'application/json'}, body: JSON.stringify(body || {})});
  return r.json();
}
function toast(msg, ms = 2200) { const t = $('#toast'); t.textContent = msg; t.classList.add('on'); clearTimeout(t._t); t._t = setTimeout(() => t.classList.remove('on'), ms); }
async function copy(text, what) {
  try { await navigator.clipboard.writeText(text); }
  catch { const a = document.createElement('textarea'); a.value = text; document.body.appendChild(a); a.select(); document.execCommand('copy'); a.remove(); }
  toast(what + ' скопирован');
}
const esc = s => String(s).replace(/[&<>"']/g, c => ({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c]));

async function refresh() {
  try { data = (await api('list')).tunnels; render(); } catch { }
  const busy = data.some(t => t.status === 'connecting');
  clearTimeout(timer); timer = setTimeout(refresh, busy ? 1000 : 4000);
}

function render() {
  const q = $('#q').value.trim().toLowerCase();
  const up = data.filter(t => t.status === 'up').length;
  $('#count').textContent = data.length ? `${up} из ${data.length} подключено` : '';
  const rows = data.filter(t => !q || (t.name + t.host + t.url + t.lport).toLowerCase().includes(q));
  if (!rows.length) { $('#list').innerHTML = `<div class="empty">${data.length ? 'Ничего не найдено' : 'Пока пусто. Нажмите «Импорт» и вставьте свои команды из блокнота.'}</div>`; return; }
  $('#list').innerHTML = rows.map(t => {
    let btns = '';
    if (t.status === 'up') btns = `<a class="go" href="${esc(t.url)}" target="_blank" rel="noopener">Войти на сайт</a><button data-a="down" class="bad">Стоп</button>`;
    else if (t.status === 'connecting') btns = `<button disabled>Подключение…</button><button data-a="down" class="bad">Отмена</button>`;
    else if (t.status === 'invalid') btns = `<button data-a="edit" class="pri">Исправить</button>`;
    else btns = `<button data-a="up" class="pri">Подключить</button>`;
    return `<div class="row ${t.status}" data-id="${t.id}">
      <span class="dot"></span>
      <div>
        <div class="name">${esc(t.name)}${t.external ? '<span class="badge" title="Запущен не из панели">внешний</span>' : ''}</div>
        <div class="sub">${esc(t.user ? t.user + '@' : '')}${esc(t.host)}:${esc(t.sshPort)} · localhost:${t.lport} → ${esc(t.target)}</div>
        <div><a class="url" href="${esc(t.url)}" target="_blank" rel="noopener">${esc(t.url)}</a></div>
      </div>
      <div class="act">${btns}
        ${t.hasPassword ? '<button class="ghost" data-a="pw" title="Копировать пароль">🔑</button>' : ''}
        <button class="ghost" data-a="url" title="Копировать ссылку">🔗</button>
        <button class="ghost" data-a="edit" title="Изменить">✎</button>
      </div>
      ${(t.status === 'failed' || t.status === 'invalid') && t.error ? `<div class="err">${esc(t.error)}</div>` : ''}
    </div>`;
  }).join('');
}

$('#list').addEventListener('click', async e => {
  const b = e.target.closest('button[data-a]'); if (!b) return;
  const id = b.closest('.row').dataset.id, t = data.find(x => x.id === id), a = b.dataset.a;
  if (a === 'up') {
    t.status = 'connecting'; render();
    const r = await api('up', {id}); if (!r.ok) toast(r.error, 5000); refresh();
  } else if (a === 'down') { await api('down', {id}); refresh(); }
  else if (a === 'pw') copy((await api('password', {id})).password, 'Пароль');
  else if (a === 'url') copy(t.url, 'Ссылка');
  else if (a === 'edit') openEdit(id);
});

$('#upall').onclick = async () => { const r = await api('upall'); if (!r.ok) toast(r.error, 6000); refresh(); };
$('#downall').onclick = async () => { await api('downall'); refresh(); };
$('#q').oninput = render;
document.addEventListener('keydown', e => { if (e.key === '/' && !/INPUT|TEXTAREA/.test(document.activeElement.tagName)) { e.preventDefault(); $('#q').focus(); } });

/* ----- добавить / изменить ----- */
let hintT;
async function checkCmd() {
  const c = $('#cmd').value.trim(), h = $('#hint');
  if (!c) { h.className = 'hint'; h.textContent = ''; return; }
  const r = await api('parse', {command: c});
  if (r.error) { h.className = 'hint e'; h.textContent = r.error; return; }
  h.className = 'hint g';
  h.textContent = `✓ ${r.user ? r.user + '@' : ''}${r.host}:${r.sshPort}  ·  localhost:${r.lport} → ${r.target}`;
  if (!$('#url').value || $('#url').dataset.auto === '1') { $('#url').value = ''; $('#url').placeholder = 'http://localhost:' + r.lport; }
}
$('#cmd').oninput = () => { clearTimeout(hintT); hintT = setTimeout(checkCmd, 250); };
$('#eye').onclick = () => { const p = $('#pw'); p.type = p.type === 'password' ? 'text' : 'password'; $('#eye').textContent = p.type === 'password' ? 'Показать' : 'Скрыть'; };
$('#add').onclick = () => { editing = null; $('#dtitle').textContent = 'Новый туннель'; ['#cmd', '#name', '#pw', '#url'].forEach(s => $(s).value = ''); $('#del').hidden = true; $('#hint').textContent = ''; $('#dlg').showModal(); $('#cmd').focus(); };
async function openEdit(id) {
  const t = await api('get', {id}); editing = id;
  $('#dtitle').textContent = 'Изменить туннель';
  $('#cmd').value = t.command; $('#name').value = t.name; $('#pw').value = t.password; $('#url').value = t.url;
  $('#pw').type = 'password'; $('#eye').textContent = 'Показать';
  $('#del').hidden = false; $('#dlg').showModal(); checkCmd();
}
$('#cancel').onclick = () => $('#dlg').close();
$('#f').onsubmit = async e => {
  e.preventDefault();
  const r = await api('save', {id: editing || '', command: $('#cmd').value, name: $('#name').value, password: $('#pw').value, url: $('#url').value});
  if (r.error) { $('#hint').className = 'hint e'; $('#hint').textContent = r.error; return; }
  $('#dlg').close(); refresh();
};
$('#del').onclick = async () => {
  if (!confirm('Удалить туннель (и отключить его)?')) return;
  await api('delete', {id: editing}); $('#dlg').close(); refresh();
};

/* ----- импорт ----- */
$('#imp').onclick = () => { $('#itext').value = ''; $('#ihint').textContent = ''; $('#idlg').showModal(); $('#itext').focus(); };
$('#icancel').onclick = () => $('#idlg').close();
$('#idlg form').onsubmit = async e => {
  e.preventDefault();
  const r = await api('import', {text: $('#itext').value});
  if (r.bad.length) { $('#ihint').textContent = 'Не распознано:\n' + r.bad.join('\n'); if (!r.added) return; }
  $('#idlg').close(); toast('Добавлено: ' + r.added); refresh();
};

refresh();
</script>
</body>
</html>
