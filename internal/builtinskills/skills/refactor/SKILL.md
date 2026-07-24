---
name: refactor
description: "Expert code refactoring based on Martin Fowler's catalog — improve maintainability without changing behavior. Covers code smells, composing methods, moving features, organizing data, simplifying conditionals, method calls, and generalization. Triggers on: refactor, 重构, clean up, improve code, code smell, extract method, rename, simplify."
user-invocable: true
---

# Refactor — Expert Code Restructuring

Surgical code refactoring based on Martin Fowler's <Refactoring> (2nd Edition) catalog. Improve structure, readability, and maintainability without changing external behavior. Gradual evolution, not revolution.

---

## When to Use

This skill activates when:
- Code is hard to understand or maintain
- Functions/classes have grown too large
- Code smells are detected
- Adding features is difficult due to poor structure
- User explicitly requests refactoring, cleanup, or improvement
- User says: refactor, 重构, clean up, improve code, code smell, extract method, rename, simplify

---

## The Golden Rules

These five rules are non-negotiable. Violating any of them turns refactoring into reckless editing.

### 1. Behavior is Preserved

Only *how* the code works changes, never *what* it does. If tests existed before, they must pass after. If the refactoring introduces a behavioral change, it's not refactoring — it's rewriting.

### 2. Small Steps

Each change should be the smallest possible transformation that compiles and passes tests. If a step breaks, you know exactly which change caused it. Refactoring is a series of tiny, safe transformations, not one big rewrite.

### 3. Version Control is Your Friend

Commit before starting. Commit after each successful step. This gives you infinite undo. Branch from a clean state so you can abandon the refactoring without consequences.

### 4. Tests are Essential

"Without tests, you're not refactoring — you're just editing." If tests don't exist for the target code, write characterization tests first. These tests capture the current behavior so you can detect regressions.

### 5. One Thing at a Time

Never mix refactoring with feature changes. Never refactor two unrelated things simultaneously. Each commit should contain exactly one refactoring operation.

---

## When NOT to Refactor

| Scenario | Action |
|----------|--------|
| Code works and won't change again | Leave it alone |
| Critical production path with no tests | Write characterization tests first |
| Under tight deadline pressure | Document the smell, refactor later |
| No clear purpose or benefit | Don't refactor for refactoring's sake |
| Code is fundamentally wrong | This is a rewrite, not a refactoring |

---

## Code Smells Catalog

Based on Fowler's taxonomy. Before refactoring, identify which smell is present.

### Bloaters

| Smell | Description | Primary Refactoring |
|-------|-------------|-------------------|
| **Long Method** | Method > 10-15 lines, doing multiple things | Extract Method, Replace Temp with Query |
| **Large Class** | Class with too many fields/methods (God Object) | Extract Class, Extract Subclass |
| **Primitive Obsession** | Using primitives instead of small objects | Replace Data Value with Object, Replace Type Code with Class |
| **Long Parameter List** | Method with > 3-4 parameters | Introduce Parameter Object, Preserve Whole Object |
| **Data Clumps** | Same group of data appearing together | Extract Class, Introduce Parameter Object |

### Object-Orientation Abusers

| Smell | Description | Primary Refactoring |
|-------|-------------|-------------------|
| **Switch Statements** | Repeated switch/if-else on type codes | Replace Conditional with Polymorphism, Replace Type Code with Subclasses |
| **Temporary Field** | Field only set in certain circumstances | Extract Class, Introduce Null Object |
| **Refused Bequest** | Subclass doesn't use inherited members | Replace Inheritance with Delegation, Push Down Method/Field |
| **Alternative Classes with Different Interfaces** | Classes doing similar things with different names | Rename Method, Move Method, Extract Superclass |

### Change Preventers

| Smell | Description | Primary Refactoring |
|-------|-------------|-------------------|
| **Divergent Change** | One class changed for different reasons | Extract Class |
| **Shotgun Surgery** | One change requires many small changes across classes | Move Method, Move Field, Inline Class |
| **Parallel Inheritance Hierarchies** | Adding a subclass to one hierarchy forces adding to another | Move Method, Move Field |

### Dispensables

| Smell | Description | Primary Refactoring |
|-------|-------------|-------------------|
| **Comments** | Comments explaining what code does (not why) | Extract Method, Rename Variable, Introduce Assertion |
| **Duplicate Code** | Same code structure in multiple places | Extract Method, Pull Up Method, Form Template Method |
| **Lazy Class** | Class doing too little to justify existence | Inline Class, Collapse Hierarchy |
| **Data Class** | Class with only fields and getters/setters | Move Method, Encapsulate Field, Encapsulate Collection |
| **Dead Code** | Unused code, imports, commented-out blocks | Delete it (git history has it) |
| **Speculative Generality** | Code built for "someday" that never came | Inline Class, Collapse Hierarchy, Remove Parameter |

### Couplers

| Smell | Description | Primary Refactoring |
|-------|-------------|-------------------|
| **Feature Envy** | Method uses another class's data more than its own | Move Method, Extract Method + Move Method |
| **Inappropriate Intimacy** | Classes know too much about each other's internals | Move Method, Move Field, Replace Delegation with Hidden Delegate |
| **Message Chains** | `a.getB().getC().getD().doSomething()` | Hide Delegate, Extract Method |
| **Middle Man** | Class delegates everything to another class | Remove Middle Man, Inline Method |
| **Incomplete Library Class** | Library missing methods you need | Introduce Foreign Method, Introduce Local Extension |

---

## Refactoring Techniques Catalog

Organized by category, from Fowler's catalog. Each technique includes its mechanical steps.

### Composing Methods

#### Extract Method
Turn a code fragment into a method whose name explains its purpose.

**Mechanics:**
1. Create a new method named after what the fragment does (not how)
2. Copy the extracted code into the new method
3. Identify local variables: read-only become parameters, modified become return values
4. Pass parameters and handle return values
5. Replace the original fragment with a call to the new method
6. Test

**Before:**
```java
void printOwing() {
    printBanner();
    // Print details
    System.out.println("name: " + _name);
    System.out.println("amount: " + getOutstanding());
}
```

**After:**
```java
void printOwing() {
    printBanner();
    printDetails(getOutstanding());
}

void printDetails(double outstanding) {
    System.out.println("name: " + _name);
    System.out.println("amount: " + outstanding);
}
```

#### Inline Method
Replace a method call with its body when the method body is as clear as the name.

**Mechanics:**
1. Check the method is not polymorphic (no subclasses override it)
2. Find all callers
3. Replace each call with the method body
4. Delete the method definition
5. Test

#### Extract Variable
Put the result of an expression (or part of it) in a self-explanatory variable.

**Before:**
```java
if (platform.toUpperCase().indexOf("MAC") > -1 &&
    browser.toUpperCase().indexOf("IE") > -1 &&
    wasInitialized() && resize > 0) {
    // ...
}
```

**After:**
```java
final boolean isMacOs = platform.toUpperCase().indexOf("MAC") > -1;
final boolean isIEBrowser = browser.toUpperCase().indexOf("IE") > -1;
final boolean wasResized = resize > 0;
if (isMacOs && isIEBrowser && wasInitialized() && wasResized) {
    // ...
}
```

#### Inline Temp
Replace a temp variable with its expression when the temp is only used once and the expression is clear.

#### Replace Temp with Query
Extract the expression into a method. Temps that are computed once and reused are replaced with method calls.

#### Split Temporary Variable
A temp assigned more than once (not loop/collecting) should be split into separate variables, one per responsibility.

#### Remove Assignments to Parameters
Don't assign to parameters. Use a local variable instead.

#### Replace Method with Method Object
When a long method uses many local variables that make Extract Method hard, turn the method into its own class, with locals as fields.

#### Substitute Algorithm
Replace an algorithm with a clearer one.

---

### Moving Features Between Objects

#### Move Method
Move a method to the class where it's used most.

**Mechanics:**
1. Check all features used by the method on its current class
2. Check for polymorphism (subclass/superclass methods)
3. Create the method on the target class, adapting as needed
4. Reference the target object from the source
5. Turn the source method into a delegating method, or remove it
6. Test

#### Move Field
Move a field to the class where it's used most.

#### Extract Class
When a class does the work of two, split it. Create a new class and move relevant fields and methods.

#### Inline Class
When a class does almost nothing, absorb it into the class that uses it most.

#### Hide Delegate
Create methods on the server to hide the delegate chain. `manager = person.getDepartment().getManager()` → `manager = person.getManager()`.

#### Remove Middle Man
When a class is doing too much delegation, call the delegate directly.

#### Introduce Foreign Method
When a server class needs an additional method but you can't modify it, create a method on the client with the server instance as the first argument.

#### Introduce Local Extension
When you need multiple foreign methods, create an extension class (subclass or wrapper).

---

### Organizing Data

#### Self Encapsulate Field
Access fields through getters and setters, even within the owning class.

#### Replace Data Value with Object
When a data item needs additional data or behavior, turn it into an object.

**Before:**
```java
class Order {
    private String customer;  // Just a string
}
```

**After:**
```java
class Order {
    private Customer customer;  // Rich object with name, address, credit rating
}
```

#### Change Value to Reference
When you need to share one instance of an object across multiple places.

#### Change Reference to Value
When a reference object is small, immutable, and you want value semantics.

#### Replace Array with Object
When an array holds heterogeneous data (`String[] row = new String[3]` — name, score, wins), replace with an object.

#### Duplicate Observed Data
Domain data lives in a GUI control but domain logic needs it. Copy the data into a domain object and set up an observer to keep the two in sync (Observer pattern). Separates presentation from domain so each can evolve independently.

#### Change Unidirectional Association to Bidirectional
Two classes need each other's features but only one holds a reference. Add a back-pointer and make the modifiers on both ends keep the link consistent. Add the reference only when genuinely needed — bidirectional links raise coupling and risk inconsistency.

#### Change Bidirectional Association to Unidirectional
A two-way link exists but one side no longer uses the other. Drop the unneeded direction. Reduces coupling, simplifies lifecycle management, and avoids "zombie" objects kept alive only by a stale back-pointer.

#### Replace Magic Number with Symbolic Constant
Replace literal numbers/strings with named constants.

#### Encapsulate Field
Make public fields private and provide accessors.

#### Encapsulate Collection
Never return the raw collection. Return a read-only view and provide add/remove methods.

#### Replace Type Code with Class
Replace a numeric/string type code with a class that has meaningful behavior.

#### Replace Type Code with Subclasses
When type code affects behavior, use polymorphism instead of conditionals.

#### Replace Type Code with State/Strategy
Similar to subclasses but uses composition when the type can change at runtime.

#### Replace Subclass with Fields
When subclasses vary only in constant data, replace them with fields on a single class.

---

### Simplifying Conditional Expressions

#### Decompose Conditional
Extract the condition, then-part, and else-part into separate methods.

**Before:**
```java
if (date.before(SUMMER_START) || date.after(SUMMER_END)) {
    charge = quantity * _winterRate + _winterServiceCharge;
} else {
    charge = quantity * _summerRate;
}
```

**After:**
```java
if (isSummer(date)) {
    charge = summerCharge(quantity);
} else {
    charge = winterCharge(quantity);
}
```

#### Consolidate Conditional Expression
Combine multiple conditionals that have the same result.

#### Consolidate Duplicate Conditional Fragments
Move code that appears in every branch outside the conditional.

#### Remove Control Flag
Replace control flags with break, continue, or return.

#### Replace Nested Conditional with Guard Clauses
Use early returns for special cases instead of deep nesting.

**Before (arrow code):**
```java
double getPayAmount() {
    double result;
    if (_isDead) {
        result = deadAmount();
    } else {
        if (_isSeparated) {
            result = separatedAmount();
        } else {
            if (_isRetired) {
                result = retiredAmount();
            } else {
                result = normalPayAmount();
            }
        }
    }
    return result;
}
```

**After:**
```java
double getPayAmount() {
    if (_isDead) return deadAmount();
    if (_isSeparated) return separatedAmount();
    if (_isRetired) return retiredAmount();
    return normalPayAmount();
}
```

#### Replace Conditional with Polymorphism
When a conditional chooses different behavior based on the type of an object, use subclasses.

#### Introduce Null Object
Replace null checks with a null object that provides default behavior.

#### Introduce Assertion
State assumptions explicitly with assertions.

---

### Making Method Calls Simpler

#### Rename Method
The name should say what the method does. If you can't think of a good name, the method may have multiple responsibilities.

#### Add Parameter / Remove Parameter
Add parameters when a method needs more info. Remove parameters when the method can get the info another way.

#### Separate Query from Modifier
A method should either return a value OR change state, never both.

#### Parameterize Method
Several methods doing similar things with different values → one method with a parameter.

#### Replace Parameter with Explicit Methods
The inverse: when a parameter essentially selects different behavior, create separate methods.

#### Preserve Whole Object
Pass the whole object instead of pulling individual fields from it.

#### Replace Parameter with Method
*(refactoring.guru: Replace Parameter with Method Call)* When a parameter can be computed from data the object already has, remove the parameter and let the method call the query itself.

#### Introduce Parameter Object
Group parameters that naturally go together into an object.

#### Remove Setting Method
Make a field immutable by removing its setter and setting it in the constructor.

#### Hide Method
Make methods private when they're not used outside the class.

#### Replace Constructor with Factory Method
When you need more flexibility than a simple constructor call.

#### Replace Error Code with Exception
Throw an exception instead of returning an error code.

#### Replace Exception with Test
Check the condition first instead of catching an exception.

---

### Dealing with Generalization

#### Pull Up Field/Method/Constructor Body
Move identical fields/methods/constructor code from subclasses to superclass.

#### Push Down Method/Field
Move behavior from superclass to only the subclasses that use it.

#### Extract Subclass
Create a subclass for a subset of features used in some instances.

#### Extract Superclass
Create a superclass for shared features of similar classes.

#### Extract Interface
Create an interface from a subset of a class's public methods.

#### Collapse Hierarchy
Merge a superclass and subclass when they're not different enough.

#### Form Template Method
Generalize an algorithm in the superclass, letting subclasses fill in the specifics.

#### Replace Inheritance with Delegation
When a subclass only uses part of the superclass, use composition instead.

#### Replace Delegation with Inheritance
When a delegating class needs access to all of the delegate's behavior.

---

## The Refactoring Process

### Phase 1: Prepare

1. **Write characterization tests** if they don't exist. These capture current behavior — they don't need to be elegant, just comprehensive enough to catch regressions.
2. **Commit** current state. Start from a clean working tree.
3. **Create a branch** for the refactoring. Keep it separate from feature work.

### Phase 2: Identify

1. **Smell the code.** Use the smell catalog above to classify what's wrong.
2. **Understand the code.** Read it thoroughly. You must understand what it does before changing it.
3. **Choose the right refactoring.** Pick from the technique catalog. Know what the result looks like before you start.

### Phase 3: Refactor (Small Steps)

For each step:
1. **Make one small change.** One refactoring technique at a time.
2. **Compile.** The code should compile after every change.
3. **Run tests.** All tests must pass. If they don't, you've changed behavior.
4. **Commit.** Create a commit with a message like `refactor: extract validateEmail method`.

Repeat until the smell is resolved.

### Phase 4: Verify

1. **All tests pass.** Non-negotiable.
2. **Manual check.** Briefly run the application or review the diff for unintended changes.
3. **Performance.** Ensure no performance regression. Simple refactorings rarely cause them, but check.

### Phase 5: Clean Up

1. **Remove stale comments.** If a refactoring made a comment obvious, delete the comment.
2. **Check for dead code.** After refactorings, unused code may emerge.
3. **Final commit.** Summarize the refactoring sequence.

---

## Refactoring Checklist

### Code Quality
- [ ] Functions are small (< 20 lines preferred, < 50 lines max)
- [ ] Each function does one thing (single responsibility)
- [ ] No duplicated code (DRY)
- [ ] Names describe what, not how
- [ ] No magic numbers or strings
- [ ] Dead code removed

### Structure
- [ ] Related code is grouped together
- [ ] Module boundaries are clear
- [ ] Dependencies flow in one direction (no cycles)
- [ ] No circular dependencies

### Conditionals
- [ ] Guard clauses replace deep nesting
- [ ] Complex conditions extracted to named methods
- [ ] Polymorphism replaces type-switching conditionals
- [ ] Null Object pattern where appropriate

### Type Safety (typed languages)
- [ ] Types defined for all public APIs
- [ ] No `any` usage without qualification
- [ ] Nullable types explicitly marked
- [ ] Type codes replaced with classes/enums

### Testing
- [ ] Refactored code is tested
- [ ] Edge cases are covered
- [ ] All tests pass after each step
- [ ] Characterization tests capture pre-refactoring behavior

---

## Language-Specific Guidance

### Java
- Prefer `final` for locals that shouldn't change
- Use IDE automated refactorings (Eclipse/IntelliJ) for mechanical steps
- Leverage the type system: enums, records (Java 14+), sealed classes (Java 17+)

### JavaScript/TypeScript
- Use destructuring to reduce parameter count
- Prefer `const` over `let` for immutable bindings
- Use TypeScript union types instead of type codes
- Nullish coalescing (`??`) and optional chaining (`?.`) eliminate null-check noise

### Python
- Use type hints for documenting intent during refactoring
- Use `dataclasses` to replace tuple/data-class patterns
- Use `@property` to replace getters
- Context managers for resource cleanup patterns

### Go
- Small interfaces preferred: accept interfaces, return structs
- Use named return values when they improve clarity
- Table-driven tests pair well with refactoring
- Avoid deep nesting with early returns

### Rust
- Use `Result` and `Option` instead of error codes and null
- Pattern matching replaces if-else chains
- `From` trait implementations clean up type conversions
- Derive macros reduce boilerplate

---

## Common Refactoring Sequences

### Extract Method Sequence
1. Create a new method named after intent
2. Copy code fragment into new method
3. Identify local variables → parameters / return values
4. Call new method from original location
5. Test

### Replace Conditional with Polymorphism Sequence
1. Create subclasses for each variant
2. Create a factory method that returns the right subclass
3. Move the conditional body to the appropriate subclass method
4. Delete the conditional

### Extract Class Sequence
1. Identify a coherent subset of fields and methods
2. Create a new class
3. Create an instance from the old class
4. Move fields and methods one at a time
5. Update references in old class
6. Test after each move

### Inline Class Sequence
1. Identify all callers of the target class
2. Move all methods/fields to the absorbing class
3. Redirect all references to the absorbing class
4. Delete the empty class
5. Test

---

## Safety Protocol

### Before You Touch Anything
```
1. Characterization tests  →  capture what the code does now
2. Git commit              →  save a known-good state
3. Branch                  →  isolate refactoring from other work
```

### Every Single Step
```
1. One change              →  one refactoring technique
2. Compile                 →  must compile clean
3. Tests                   →  every test must pass
4. Commit                  →  message: "refactor: <technique> <what>"
```

### If Tests Break
```
1. Undo the last change
2. Understand what broke and why
3. Try a smaller step
4. If the test was wrong and behavior was correct, fix the test FIRST, then retry
```

### On Completion
```
1. Full test suite         →  all tests pass
2. Manual smoke test       →  quick sanity check
3. Self-review diff        →  catch unintended changes
4. Final commit            →  describe the overall transformation
```

---

## Design Patterns in Refactoring

### Strategy Pattern
Replace a conditional that chooses an algorithm. **Smell:** Switch on type code with different behavior per branch. **Technique:** Replace Conditional with Polymorphism + Extract Method.

### Template Method
Extract common algorithm skeleton to superclass, letting subclasses fill in the variants. **Smell:** Duplicate code with slight variations. **Technique:** Form Template Method.

### State Pattern
Replace a state-based conditional by extracting each state's behavior into a class. **Smell:** Switch on status field with behavior variation. **Technique:** Replace Type Code with State/Strategy.

### Composite Pattern
Treat individual objects and groups uniformly. **Smell:** Client code has special handling for single vs. collection cases. **Technique:** Extract Interface + Create Composite.

### Decorator Pattern
Add behavior dynamically by wrapping objects. **Smell:** Conditional logic for optional behaviors. **Technique:** Extract Class + use composition.

### Null Object Pattern
Replace null checks with a default object. **Smell:** Repeated `if (x == null)` checks. **Technique:** Introduce Null Object.

---

## Edge Cases & Gotchas

| Scenario | Handling |
|----------|----------|
| No tests exist | Write characterization tests first. Run the code with various inputs, capture outputs. These are your safety net. |
| Refactoring breaks a distant test | FIRST understand why. Maybe the test relied on implementation detail. If so, fix the test to test behavior, not implementation. Then resume. |
| User wants behavior change + refactor together | REFUSE. Do them separately. Refactor first to make the behavior change easy, commit, then change behavior. |
| Method is too complex to step through | Use Replace Method with Method Object. Turn the whole method into a class where each step can be extracted. |
| Refactoring across a large codebase | Extract a micro-service or module boundary first. Then refactor within the boundary. "There is a refactoring for everything except too many refactorings." |
| IDE automated refactoring available | Use it. Modern IDEs can safely rename, extract method, introduce variable, etc. Only do it manually when the IDE can't. |
| Undo needed | `git stash` or `git reset --hard` back to last commit. Small commits make this painless. |

---

## Resources

- Martin Fowler, *Refactoring: Improving the Design of Existing Code* (2nd Edition, 2018)
- [refactoring.com](https://refactoring.com) — Fowler's online catalog
- [refactoring.guru](https://refactoring.guru) — Illustrated refactoring patterns
- [refactoring.guru/refactoring/catalog](https://refactoring.guru/refactoring/catalog) — full technique catalog (6 categories, 66 techniques) and code-smell taxonomy this skill mirrors