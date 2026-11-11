"""Find the first bindable TCP port on 127.0.0.1 from a candidate list.

Windows (Hyper-V / WSL / Docker) may reserve port ranges that cannot be
bound; this helper skips those and prints the first usable port.
Prints 0 if none of the candidates are available.
"""
import socket

CANDIDATES = [8000, 8080, 8888, 5000, 3000, 9000]


def main() -> None:
    for port in CANDIDATES:
        sock = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
        try:
            sock.bind(("127.0.0.1", port))
        except OSError:
            continue
        finally:
            sock.close()
        print(port)
        return
    print(0)


if __name__ == "__main__":
    main()
