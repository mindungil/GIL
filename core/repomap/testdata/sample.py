import os

def greet(name):
    return f"Hello {name}"

class Greeter:
    def __init__(self, prefix):
        self.prefix = prefix
    def greet(self, name):
        return self.prefix + " " + name

g = Greeter("Hi")
print(greet("world"))
