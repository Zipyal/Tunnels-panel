<?php
declare(strict_types=1);

/**
 * Запуск, остановка и статусы ssh-туннелей.
 *
 * Работаем только в Linux-контейнере, поэтому всё определяем через /proc:
 * это обычное чтение файлов, без запуска tasklist/netstat/lsof, как было
 * в первой версии (из-за них страница и тормозила).
 */
final class Tunnels
{
    /** Порты, которые сейчас слушаются. Заполняется лениво, один раз на запрос */
    private ?array $listening = null;

    /**
     * @param array{0:int,1:int}|null $portRange опубликованный в docker-compose диапазон портов
     */
    public function __construct(
        private readonly Store $store,
        private readonly ?array $portRange,
    ) {
    }

    /** Данные туннеля + текущий статус для интерфейса */
    public function view(array $t): array
    {
        $cmd = SshCommand::parse($t['command']);
        $view = [
            'id'          => $t['id'],
            'name'        => $t['name'],
            'command'     => $t['command'],
            'hasPassword' => $t['password'] !== '',
            'url'         => $t['url'] !== '' ? $t['url'] : ($cmd->localPort() ? 'http://localhost:' . $cmd->localPort() : ''),
            'localPort'   => $cmd->localPort(),
            'host'        => $cmd->host,
            'user'        => $cmd->user,
            'sshPort'     => $cmd->port ?: '22',
            'target'      => $cmd->target,
            'status'      => 'down', // down | connecting | up | failed | invalid
            'error'       => $cmd->error,
            'warn'        => null,
        ];

        if ($cmd->error) {
            $view['status'] = 'invalid';
            return $view;
        }
        $view['warn'] = $this->portProblem($cmd);

        $state = $this->store->read('state')[$t['id']] ?? null;
        if (isset($this->listeningPorts()[$cmd->localPort()])) {
            $view['status'] = 'up';
        } elseif ($state) {
            if ($this->isAlive((int)$state['pid'])) {
                $view['status'] = 'connecting'; // ssh работает, порт ещё не открыт
            } else {
                // Процесс умер, а мы его не останавливали: пароль не подошёл, порт занят, сервер недоступен
                $view['status'] = 'failed';
                $view['error'] = $this->logTail($t['id']) ?: 'Процесс ssh завершился без сообщения';
            }
        }
        return $view;
    }

    /** @return array{ok:bool,error?:string} */
    public function start(array $t): array
    {
        $cmd = SshCommand::parse($t['command']);
        if ($cmd->error) {
            return ['ok' => false, 'error' => $cmd->error];
        }
        if ($problem = $this->portProblem($cmd)) {
            return ['ok' => false, 'error' => $problem];
        }

        // Уже работает или запускается - ничего не делаем
        if (isset($this->listeningPorts()[$cmd->localPort()])) {
            return ['ok' => true];
        }
        $state = $this->store->read('state')[$t['id']] ?? null;
        if ($state && $this->isAlive((int)$state['pid'])) {
            return ['ok' => true];
        }

        $pid = $this->launch($t, $cmd);
        if ($pid === 0) {
            return ['ok' => false, 'error' => 'Не удалось запустить ssh'];
        }
        $this->store->update('state', function (array $state) use ($t, $pid) {
            $state[$t['id']] = ['pid' => $pid, 'started' => time()];
            return $state;
        });
        return ['ok' => true];
    }

    public function stop(array $t): void
    {
        $state = $this->store->read('state')[$t['id']] ?? null;
        $cmd = SshCommand::parse($t['command']);

        if ($state && $this->isAlive((int)$state['pid'])) {
            $this->kill((int)$state['pid']);
        } elseif (!$cmd->error && isset($this->listeningPorts()[$cmd->localPort()])) {
            // Запись о запуске потерялась, а порт открыт: ищем ssh по его аргументам
            exec('pkill -f ' . escapeshellarg('ssh .*-L 0\.0\.0\.0:' . $cmd->localPort() . ':'));
        }
        $this->store->update('state', function (array $state) use ($t) {
            unset($state[$t['id']]);
            return $state;
        });
    }

    // ---------------------------------------------------------------- внутреннее

    /** Запускает ssh в фоне и возвращает его pid (0 - не получилось) */
    private function launch(array $t, SshCommand $cmd): int
    {
        $log = $this->store->logPath($t['id']);
        $args = $cmd->args($t['password'] !== '', $this->store->dir() . '/known_hosts');
        $line = array_map('escapeshellarg', [getenv('SSH_BIN') ?: 'ssh', ...$args]);

        // nohup + setsid: ssh не умирает вместе с запросом, stdout/stderr уходят в лог.
        // "echo $!" печатает pid фонового процесса
        $shell = 'cd / && nohup setsid ' . implode(' ', $line)
            . ' >' . escapeshellarg($log) . ' 2>&1 </dev/null & echo $!';

        // Пароль отдаём через окружение: askpass.sh его печатает, в аргументах ssh пароля нет
        putenv('SSH_PW=' . $t['password']);
        $pid = (int)shell_exec($shell);
        putenv('SSH_PW');
        return $pid;
    }

    /** Все порты в состоянии LISTEN одним чтением /proc/net/tcp (вместо сокета на каждый порт) */
    private function listeningPorts(): array
    {
        if ($this->listening !== null) {
            return $this->listening;
        }
        $this->listening = [];
        foreach (['/proc/net/tcp', '/proc/net/tcp6'] as $file) {
            $lines = @file($file, FILE_IGNORE_NEW_LINES | FILE_SKIP_EMPTY_LINES) ?: [];
            array_shift($lines); // заголовок таблицы
            foreach ($lines as $line) {
                // колонки: sl local_address rem_address st ...; st=0A это LISTEN
                $cols = preg_split('/\s+/', trim($line));
                if (($cols[3] ?? '') === '0A') {
                    $port = hexdec(substr($cols[1], strrpos($cols[1], ':') + 1));
                    $this->listening[(int)$port] = true;
                }
            }
        }
        return $this->listening;
    }

    private function isAlive(int $pid): bool
    {
        if ($pid <= 0) {
            return false;
        }
        $stat = @file_get_contents("/proc/$pid/stat");
        if ($stat === false) {
            return false;
        }
        // Состояние идёт после ")": Z - зомби, процесс уже завершился
        if (($stat[strrpos($stat, ')') + 2] ?? '') === 'Z') {
            return false;
        }
        // Защита от повторного использования pid чужим процессом
        return str_contains((string)@file_get_contents("/proc/$pid/cmdline"), 'ssh');
    }

    private function kill(int $pid): void
    {
        if (function_exists('posix_kill')) {
            posix_kill($pid, SIGTERM);
        } else {
            exec('kill ' . $pid);
        }
    }

    /** Последние строки лога ssh без шума, это и есть текст ошибки для пользователя */
    private function logTail(string $id): string
    {
        $raw = @file_get_contents($this->store->logPath($id)) ?: '';
        $lines = array_filter(
            array_map('trim', preg_split('/\R/', $raw)),
            fn($l) => $l !== '' && !str_contains($l, 'Permanently added')
        );
        return implode("\n", array_slice($lines, -4));
    }

    /** Порт вне опубликованного диапазона недоступен с хоста, предупреждаем заранее */
    private function portProblem(SshCommand $cmd): ?string
    {
        if (!$this->portRange) {
            return null;
        }
        [$min, $max] = $this->portRange;
        foreach ($cmd->localPorts as $port) {
            if ($port < $min || $port > $max) {
                return "Порт $port не проброшен из Docker. Допустимы $min-$max, "
                    . 'диапазон меняется в файле .env (TUNNEL_PORTS), потом перезапуск.';
            }
        }
        return null;
    }
}
