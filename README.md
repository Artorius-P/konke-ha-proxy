# konke-ha-proxy
A homeassistant proxy for konke devices

## How to use?
1. modify config.yaml
2. check and fill in your konke device id and its name in homeassistant
    > you can get all your devices by this script:
    ```python
    import socket
    import json
    import time
    def send_request(host='YourKonkeGatewayIP', port=5000, messages=[]):
        with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as s:
            s.settimeout(1)
            s.connect((host, port))
            result = []
            for message in messages:
                print(f"Sending: {message}")
                try:
                    s.sendall(message.encode())
                    data = s.recv(1024)
                    print(f"Received: {data.decode()}")
                    result.append(data.decode()+"\n")
                except socket.timeout:
                    print("timeout")
            with open("result.txt", "w") as f:
                f.writelines(result)


    if __name__ == "__main__":
        requests = [
            "!{\"nodeid\":\"*\",\"opcode\":\"LOGIN\",\"requester\":\"HJ_Server\",\"arg\":{\"username\":\"admin\",\"seq\":\"\",\"device\":\"\",\"password\":\"admin\",\"zkid\":\"266590\",\"version\":\"\"}}$",
        ]
        for nodeid in range(1, 200):
            r = "!"+json.dumps(
                {
                "nodeid": f"{nodeid}",
                "opcode": "QUERY",
                "arg": "*",
                "requester": "HJ_Server",
                "reqId": int(time.time()),
            })+"$"
            requests.append(r)
        send_request(messages=requests)

    ```
3. modify configuration.yaml in your homeassistant

## Build Instruction

```golang
CGO_ENABLED=0 go build
```