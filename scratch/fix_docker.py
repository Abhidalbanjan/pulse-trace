import glob

for f in glob.glob('*/Dockerfile'):
    with open(f, 'r') as file:
        content = file.read()
    if 'apk add' not in content:
        content = content.replace('ENV GOPROXY=direct\nRUN go mod download', 'ENV GOPROXY=direct\nRUN apk add --no-cache git\nRUN go mod download')
        with open(f, 'w') as file:
            file.write(content)
    print(f"Fixed git issue in {f}")
