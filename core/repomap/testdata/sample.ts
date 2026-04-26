export interface Greeter {
    greet(name: string): string;
}

export class Hello implements Greeter {
    constructor(public prefix: string) {}
    greet(name: string): string {
        return this.prefix + " " + name;
    }
}

export const make = (p: string) => new Hello(p);
