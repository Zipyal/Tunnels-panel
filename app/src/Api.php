<?php
declare(strict_types=1);

/** Ошибка, которую показываем пользователю (код = http-статус) */
final class ApiError extends RuntimeException
{
}

/** Обработчики запросов /api/<action>. Роутинг и заголовки живут в index.php */
final class Api
{
    public function __construct(
        private readonly Store $store,
        private readonly Tunnels $tunnels,
    ) {
    }

    public function handle(string $action, array $in): array
    {
        $id = (string)($in['id'] ?? '');

        return match ($action) {
            'list'     => ['tunnels' => array_map([$this->tunnels, 'view'], $this->store->read('tunnels'))],
            'parse'    => $this->parse((string)($in['command'] ?? '')),
            'get'      => $this->find($id),
            'password' => ['password' => $this->find($id)['password']],
            'save'     => $this->save($in),
            'delete'   => $this->delete($id),
            'up'       => $this->tunnels->start($this->find($id)),
            'down'     => $this->down($id),
            'upall'    => $this->upAll(),
            'downall'  => $this->downAll(),
            'import'   => $this->import((string)($in['text'] ?? '')),
            default    => throw new ApiError('unknown action', 404),
        };
    }

    private function find(string $id): array
    {
        foreach ($this->store->read('tunnels') as $t) {
            if ($t['id'] === $id) {
                return $t;
            }
        }
        throw new ApiError('Туннель не найден', 404);
    }

    private function parse(string $line): array
    {
        $cmd = SshCommand::parse($line);
        if ($cmd->error) {
            return ['error' => $cmd->error];
        }
        return [
            'host'      => $cmd->host,
            'user'      => $cmd->user,
            'sshPort'   => $cmd->port ?: '22',
            'localPort' => $cmd->localPort(),
            'target'    => $cmd->target,
        ];
    }

    private function save(array $in): array
    {
        $command = trim((string)($in['command'] ?? ''));
        $cmd = SshCommand::parse($command);
        if ($cmd->error) {
            throw new ApiError($cmd->error, 422);
        }
        $record = [
            'name'     => trim((string)($in['name'] ?? '')) ?: self::defaultName($cmd),
            'command'  => $command,
            'password' => (string)($in['password'] ?? ''),
            'url'      => self::normalizeUrl((string)($in['url'] ?? '')),
        ];
        $id = (string)($in['id'] ?? '');

        $this->store->update('tunnels', function (array $all) use ($id, $record) {
            foreach ($all as $i => $t) {
                if ($id !== '' && $t['id'] === $id) {
                    $all[$i] = ['id' => $id] + $record;
                    return $all;
                }
            }
            $all[] = ['id' => bin2hex(random_bytes(4))] + $record;
            return $all;
        });
        return ['ok' => true];
    }

    private function delete(string $id): array
    {
        $this->tunnels->stop($this->find($id)); // сначала гасим процесс, потом удаляем запись
        $this->store->update('tunnels', fn(array $all) => array_values(array_filter($all, fn($t) => $t['id'] !== $id)));
        return ['ok' => true];
    }

    private function down(string $id): array
    {
        $this->tunnels->stop($this->find($id));
        return ['ok' => true];
    }

    private function upAll(): array
    {
        $errors = [];
        foreach ($this->store->read('tunnels') as $t) {
            $result = $this->tunnels->start($t);
            if (!$result['ok']) {
                $errors[] = $t['name'] . ': ' . $result['error'];
            }
        }
        return ['ok' => !$errors, 'error' => implode("\n", $errors)];
    }

    private function downAll(): array
    {
        foreach ($this->store->read('tunnels') as $t) {
            $this->tunnels->stop($t);
        }
        return ['ok' => true];
    }

    /**
     * Импорт из блокнота, по строке на туннель:
     *   команда
     *   название | команда | пароль | ссылка
     */
    private function import(string $text): array
    {
        $added = 0;
        $bad = [];

        $this->store->update('tunnels', function (array $all) use ($text, &$added, &$bad) {
            foreach (preg_split('/\R/', $text) as $line) {
                $line = trim($line);
                if ($line === '') {
                    continue;
                }
                $fields = array_map('trim', explode('|', $line));
                if (count($fields) >= 2) {
                    [$name, $command, $password, $url] = array_pad($fields, 4, '');
                } else {
                    [$name, $command, $password, $url] = ['', $fields[0], '', ''];
                }

                $cmd = SshCommand::parse($command);
                if ($cmd->error) {
                    $bad[] = "$line - {$cmd->error}";
                    continue;
                }
                $all[] = [
                    'id'       => bin2hex(random_bytes(4)),
                    'name'     => $name ?: self::defaultName($cmd),
                    'command'  => $command,
                    'password' => $password,
                    'url'      => self::normalizeUrl($url),
                ];
                $added++;
            }
            return $all;
        });
        return ['added' => $added, 'bad' => $bad];
    }

    private static function defaultName(SshCommand $cmd): string
    {
        return ($cmd->user ? "{$cmd->user}@" : '') . $cmd->host . ':' . $cmd->localPort();
    }

    /** "localhost:1111" -> "http://localhost:1111", иначе браузер откроет это как относительную ссылку */
    private static function normalizeUrl(string $url): string
    {
        $url = trim($url);
        if ($url !== '' && !preg_match('~^[a-z][a-z0-9+.-]*://~i', $url)) {
            $url = "http://$url";
        }
        return $url;
    }
}
