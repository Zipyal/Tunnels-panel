<?php
declare(strict_types=1);

/**
 * Разбор строки вида
 *   ssh -f -N -L 1111:11.11.1.11:222 login@333.333.33.33 -p22
 * Саму строку мы никогда не отдаём в shell целиком: достаём из неё параметры
 * и собираем аргументы заново (см. args()). Так в команду нельзя подсунуть
 * лишнего, например "; rm -rf".
 */
final class SshCommand
{
    // Флаги ssh, после которых идёт значение
    private const WITH_VALUE = 'bcDEeFIiJLlmOoQRSWwp';
    // Эти флаги нам не нужны: -N добавим сами, остальные про терминал
    private const IGNORED = 'fNTn';

    public string $user = '';
    public string $host = '';
    public string $port = '';
    /** @var string[] пробросы в формате bind:порт:хост:порт */
    public array $forwards = [];
    /** @var string[] остальные опции ssh, которые передаём как есть */
    public array $extra = [];
    /** @var int[] локальные порты всех пробросов */
    public array $localPorts = [];
    public string $target = '';
    public ?string $error = null;

    public function localPort(): int
    {
        return $this->localPorts[0] ?? 0;
    }

    public static function parse(string $line): self
    {
        $cmd = new self();
        $tokens = self::tokenize(trim($line));

        // Первым словом может быть сам "ssh" или "ssh.exe" - выкидываем
        if ($tokens && preg_match('~(^|[\\\\/])ssh(\.exe)?$~i', $tokens[0])) {
            array_shift($tokens);
        }

        $positional = [];
        for ($i = 0; $i < count($tokens); $i++) {
            $token = $tokens[$i];
            if ($token[0] !== '-' || strlen($token) < 2) {
                $positional[] = $token;
                continue;
            }
            // "-fN" это два флага, "-p22" это флаг со значением, поэтому идём по символам
            for ($j = 1; $j < strlen($token); $j++) {
                $flag = $token[$j];
                if (str_contains(self::WITH_VALUE, $flag)) {
                    $value = substr($token, $j + 1);
                    if ($value === '' || $value === false) {
                        $value = $tokens[++$i] ?? ''; // значение отдельным словом: "-p 22"
                    }
                    $cmd->takeOption($flag, $value);
                    break;
                }
                if (!str_contains(self::IGNORED, $flag)) {
                    $cmd->extra[] = "-$flag";
                }
            }
        }

        if (!$positional) {
            $cmd->error = 'Не нашёл user@host в команде';
            return $cmd;
        }
        $dest = $positional[0];
        if (($at = strrpos($dest, '@')) !== false) {
            $cmd->user = substr($dest, 0, $at);
            $dest = substr($dest, $at + 1);
        }
        $cmd->host = $dest;

        if ($cmd->port !== '' && !ctype_digit($cmd->port)) {
            $cmd->error = 'Некорректный порт после -p';
        } elseif (!$cmd->forwards) {
            $cmd->error = 'Не нашёл -L (проброс порта)';
        }
        return $cmd;
    }

    private function takeOption(string $flag, string $value): void
    {
        switch ($flag) {
            case 'L':
                $this->addForward($value);
                break;
            case 'p':
                $this->port = $value;
                break;
            case 'l':
                $this->user = $value;
                break;
            case 'i':
                // Путь к ключу на хосте (C:\Users\...) внутри контейнера не существует.
                // Ключи кладут в папку keys/, entrypoint копирует их в /root/.ssh
                $name = basename(str_replace('\\', '/', $value));
                array_push($this->extra, '-i', "/root/.ssh/$name");
                break;
            default:
                array_push($this->extra, "-$flag", $value);
        }
    }

    private function addForward(string $spec): void
    {
        if ($this->error !== null) {
            return;
        }
        if (str_contains($spec, '[')) {
            $this->error = 'IPv6 в -L пока не поддерживается';
            return;
        }
        $parts = explode(':', $spec);
        // 3 части: порт:хост:порт. 4 части: bind:порт:хост:порт
        if (count($parts) === 3) {
            array_unshift($parts, '0.0.0.0');
        } elseif (count($parts) === 4) {
            $parts[0] = '0.0.0.0';
        } else {
            $this->error = 'Не понял значение -L';
            return;
        }
        // Внутри контейнера слушаем на 0.0.0.0, иначе Docker не сможет
        // пробросить порт наружу. На хосте порт всё равно открыт только на 127.0.0.1
        // (см. docker-compose.yml)
        if (!ctype_digit($parts[1]) || !ctype_digit($parts[3])) {
            $this->error = 'Порты в -L должны быть числами';
            return;
        }
        $this->forwards[] = implode(':', $parts);
        $this->localPorts[] = (int)$parts[1];
        if (count($this->localPorts) === 1) {
            $this->target = "{$parts[2]}:{$parts[3]}";
        }
    }

    /**
     * Аргументы для запуска ssh.
     *
     * @return string[]
     */
    public function args(bool $withPassword, string $knownHostsFile): array
    {
        // Свои опции пользователя идут первыми: у ssh для -o побеждает первое значение
        $args = $this->extra;
        array_push(
            $args,
            '-N',
            '-o', 'ExitOnForwardFailure=yes',          // порт занят -> сразу ошибка, а не молчаливый туннель без проброса
            '-o', 'StrictHostKeyChecking=accept-new',  // новый сервер принимаем, при смене ключа ssh откажется
            '-o', "UserKnownHostsFile=$knownHostsFile",
            '-o', 'ServerAliveInterval=30',
            '-o', 'ServerAliveCountMax=3',
            '-o', 'ConnectTimeout=15'
        );
        if ($withPassword) {
            array_push($args, '-o', 'NumberOfPasswordPrompts=1', '-o', 'PreferredAuthentications=password,keyboard-interactive');
        } else {
            $args[] = '-o';
            $args[] = 'BatchMode=yes'; // без пароля ssh не должен зависать и ждать ввод
        }
        foreach ($this->forwards as $forward) {
            array_push($args, '-L', $forward);
        }
        if ($this->port !== '') {
            array_push($args, '-p', $this->port);
        }
        if ($this->user !== '') {
            array_push($args, '-l', $this->user);
        }
        $args[] = $this->host;
        return $args;
    }

    /** Слова с учётом кавычек: -i "C:\my keys\id" остаётся одним словом */
    private static function tokenize(string $line): array
    {
        preg_match_all('/"([^"]*)"|\'([^\']*)\'|(\S+)/u', $line, $m, PREG_SET_ORDER);
        $tokens = [];
        foreach ($m as $x) {
            if (isset($x[3]) && $x[3] !== '') {
                $tokens[] = $x[3];
            } elseif (isset($x[2]) && $x[2] !== '') {
                $tokens[] = $x[2];
            } else {
                $tokens[] = $x[1];
            }
        }
        return $tokens;
    }
}
