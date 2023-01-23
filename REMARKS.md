# Notes and Remarks

The `notes-and-remarks` branch is used to add comments, ask questions, and propose improvements to the code. 
We call each of these comments, questions, and proposals, a `remark`.

A `remark` is added as a comment **ABOVE** the line/block of code to which it refers to, and it starts with `"REMARK:"`.  
For instance:
```
// REMARK: This is a 'remark'
```
or
```
/* REMARK: This is another 'remark' */
```

<!-- TODO: link PR when ready -->
Remarks committed to `notes-and-remarks` can be discussed in the _Notes and Remarks_ PR.
This will be a special "ongoing" PR, with each commit including one `remark`, or multiple ones if they are related.

## Discussion and Implemention

Ideally, a single remark will have the following lifecycle:
1. The remark is committed and pushed to `notes-and-remarks`.
3. In the PR, the remark is discussed by means of `comments` (as a normal PR).
4. If the remark needs not to be addressed, it is deleted with a new commit.
5. If the remark needs to be addressed, 
    a new Issue is generate starting from one of the comments.
6. The new Issue is managed as usual (with its own PR).
7. When the Issue gets closed (i.e., its PR has been merged),  
    the `notes-and-remarks` branch is rebased to `main`, deleting the remark.

