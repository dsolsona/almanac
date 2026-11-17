"""Serve only review assets, never repository files or operator credentials.
Run: make shell SOCKET=1 CMD='python3 docs/design/mocks/maintenance/serve.py'
"""
from http.server import SimpleHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from http.client import HTTPConnection
from urllib.parse import unquote, urlsplit

ROOT = Path(__file__).resolve().parents[4]
MOCKS = ROOT / 'docs/design/mocks'
FONT = ROOT / 'web/public/fonts/Bevan-Regular.ttf'

class ReviewHandler(SimpleHTTPRequestHandler):
    def translate_path(self, path):
        requested = unquote(urlsplit(path).path)
        if requested == '/':
            return str(MOCKS / 'maintenance/index.html')
        candidate = (ROOT / requested.lstrip('/')).resolve()
        if (candidate.is_relative_to(MOCKS) and candidate.suffix in {'.html', '.css', '.js', '.png'}) or candidate == FONT:
            return str(candidate)
        return str(ROOT / '.nonexistent-review-resource')

    def do_GET(self):
        route = urlsplit(self.path).path
        if '..' in unquote(route).split('/'):
            self.send_error(404)
            return
        if route in {'/', '/docs/design/mocks/maintenance/index.html'}:
            self.send_response(302)
            self.send_header('Location', '/maintenance-preview.html')
            self.end_headers()
            return
        if route == '/maintenance-preview.html' or route == '/@react-refresh' or route.startswith(('/review-maintenance/', '/src/', '/@vite/', '/node_modules/', '/fonts/')):
            connection = HTTPConnection('127.0.0.1', 5198, timeout=10)
            connection.request('GET', self.path)
            response = connection.getresponse()
            self.send_response(response.status)
            for name, value in response.getheaders():
                if name.lower() in {'content-type', 'cache-control', 'etag'}:
                    self.send_header(name, value)
            self.end_headers()
            self.wfile.write(response.read())
            connection.close()
            return
        super().do_GET()

    def list_directory(self, path):
        self.send_error(404)

print('Maintenance review: http://127.0.0.1:5199/docs/design/mocks/maintenance/index.html', flush=True)
ThreadingHTTPServer(('0.0.0.0', 5199), ReviewHandler).serve_forever()
