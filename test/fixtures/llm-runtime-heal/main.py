from fastapi import FastAPI

# Deliberately not named "app": the scanner's default entry is main:app,
# so the generated CMD is wrong until the LLM healer repairs it.
application = FastAPI()


@application.get("/")
def root():
    return {"ok": True}