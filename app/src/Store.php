<?php
declare(strict_types=1);

/**
 * Хранилище в json-файлах:
 *   tunnels.json - список туннелей (то, что ввёл пользователь)
 *   state.json   - что сейчас запущено (pid процесса ssh)
 *
 * Для 10-20 записей SQLite не нужна, а json можно открыть и поправить руками.
 */
final class Store
{
    /** Кэш на время одного запроса, чтобы не читать один файл по нескольку раз */
    private array $cache = [];

    public function __construct(private readonly string $dir)
    {
        if (!is_dir("$dir/logs")) {
            mkdir("$dir/logs", 0700, true);
        }
    }

    public function dir(): string
    {
        return $this->dir;
    }

    public function logPath(string $id): string
    {
        return "{$this->dir}/logs/$id.log";
    }

    public function read(string $name): array
    {
        if (!isset($this->cache[$name])) {
            $raw = @file_get_contents("{$this->dir}/$name.json");
            $this->cache[$name] = $raw ? (json_decode($raw, true) ?: []) : [];
        }
        return $this->cache[$name];
    }

    /**
     * Прочитать -> изменить -> записать под одной блокировкой.
     * Сервер работает в несколько процессов, без lock два запроса
     * подряд могли бы затереть правки друг друга.
     *
     * @param callable(array): array $change получает текущие данные, возвращает новые
     */
    public function update(string $name, callable $change): void
    {
        $lock = fopen("{$this->dir}/.lock", 'c');
        flock($lock, LOCK_EX);
        try {
            unset($this->cache[$name]); // берём свежие данные с диска, а не из кэша
            $data = $change($this->read($name));

            $file = "{$this->dir}/$name.json";
            $tmp = "$file.tmp";
            file_put_contents($tmp, json_encode($data, JSON_PRETTY_PRINT | JSON_UNESCAPED_UNICODE | JSON_UNESCAPED_SLASHES));
            chmod($tmp, 0600); // в файле лежат пароли
            rename($tmp, $file); // rename атомарный, файл не окажется записанным наполовину

            $this->cache[$name] = $data;
        } finally {
            flock($lock, LOCK_UN);
            fclose($lock);
        }
    }
}
