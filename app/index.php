<?php
declare(strict_types=1);

/**
 * Точка входа (router-скрипт для встроенного сервера PHP).
 *   /            -> public/index.html
 *   /app.js ...  -> статику отдаёт сам сервер
 *   /api/<name>  -> Api
 */

require __DIR__ . '/src/Store.php';
require __DIR__ . '/src/SshCommand.php';
require __DIR__ . '/src/Tunnels.php';
require __DIR__ . '/src/Api.php';

$path = parse_url($_SERVER['REQUEST_URI'] ?? '/', PHP_URL_PATH);

// Статика: возвращаем false, и встроенный сервер сам отдаст файл из public/
if ($path !== '/' && is_file(__DIR__ . '/public' . $path)) {
    return false;
}

function respond(array $data, int $status = 200): never
{
    http_response_code($status);
    header('Content-Type: application/json; charset=utf-8');
    header('Cache-Control: no-store');
    echo json_encode($data, JSON_UNESCAPED_UNICODE | JSON_UNESCAPED_SLASHES | JSON_INVALID_UTF8_SUBSTITUTE);
    exit;
}

// Панель умеет запускать процессы, поэтому отвечаем только на localhost.
// Это защита от DNS-rebinding: чужой сайт не сможет обратиться к панели по своему домену
if (!preg_match('/^(localhost|127\.0\.0\.1)(:\d+)?$/i', $_SERVER['HTTP_HOST'] ?? '')) {
    respond(['error' => 'forbidden'], 403);
}

if ($path === '/') {
    header('Content-Type: text/html; charset=utf-8');
    readfile(__DIR__ . '/public/index.html');
    exit;
}

if (!str_starts_with($path, '/api/')) {
    respond(['error' => 'not found'], 404);
}
$action = substr($path, 5);

// Для healthcheck в docker-compose
if ($action === 'health') {
    respond(['ok' => true]);
}

// Кастомный заголовок нельзя отправить с чужого сайта без CORS-разрешения,
// так что запросы из браузера на чужих страницах сюда не пройдут
if (($_SERVER['HTTP_X_TUNNELS'] ?? '') !== '1') {
    respond(['error' => 'bad request'], 400);
}

// Диапазон портов приходит из docker-compose, например "1100-1199"
$range = null;
if (preg_match('/^(\d+)-(\d+)$/', (string)getenv('TUNNEL_PORTS'), $m)) {
    $range = [(int)$m[1], (int)$m[2]];
}

$store = new Store(getenv('DATA_DIR') ?: '/data');
$api = new Api($store, new Tunnels($store, $range));

try {
    $input = json_decode((string)file_get_contents('php://input'), true) ?: [];
    respond($api->handle($action, $input));
} catch (ApiError $e) {
    respond(['error' => $e->getMessage()], $e->getCode() ?: 400);
}
