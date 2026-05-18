from ark_memory import Agent

agent = Agent("demo")

agent.remember("User likes dark mode")

results = agent.recall("What UI theme does the user prefer?")

for r in results:
    print(r.content)
    